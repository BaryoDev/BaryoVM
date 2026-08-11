// Package update applies a health-gated container update to a stack, and puts it back if the new
// images do not come up healthy.
//
// The shape of this exists because of a real incident: a CMS release added a database index that its
// schema policy refused to apply to an existing database, so the container crash-looped on start. A
// plain "pull and restart" would have taken the site down and left it down until someone noticed. So
// an update here is: record what is running, pull, stop if nothing moved, back up, recreate, prove it
// is healthy, and otherwise return to the recorded images.
package update

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/compose"
	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Runner is the side-effecting surface, kept as an interface so the decision logic can be tested
// without an SSH connection or a container runtime.
type Runner interface {
	// Images reports what each service is currently running.
	Images(svcs []string) ([]compose.Image, error)
	// Pull fetches newer images for the given services.
	Pull(svcs []string) (string, error)
	// Up recreates the given services from whatever their references now point at.
	Up(svcs []string) (string, error)
	// Retag points a reference back at an image id.
	Retag(id, ref string) (string, error)
	// Healthy reports whether the stack is serving. Called after a recreate.
	Healthy() (bool, error)
	// Backup takes a database backup. Skipped when the stack has none configured.
	Backup() (string, error)
}

// Options controls one update.
type Options struct {
	Services []string
	// Auto marks this as an unattended run. It refuses stacks that have not opted in, and refuses
	// any stack it could not verify afterwards.
	Auto bool
	// AutoUpdate is the stack's opt-in flag.
	AutoUpdate bool
	// HasHealthCheck is false when the stack defines no health URL.
	HasHealthCheck bool
	// HasBackup is false when the stack has no database configured.
	HasBackup bool
	// SkipBackup bypasses the pre-update backup. Never set this for an unattended run.
	SkipBackup bool
	// DryRun reports whether an update is available and changes nothing.
	DryRun bool
	// HealthAttempts and HealthDelay bound how long we wait for the stack to come back.
	HealthAttempts int
	HealthDelay    time.Duration
}

// Result describes what happened, in enough detail to explain an unattended run after the fact.
type Result struct {
	Updated    bool     `json:"updated"`
	RolledBack bool     `json:"rolledBack"`
	Skipped    string   `json:"skipped,omitempty"`  // why nothing was done
	Services   []string `json:"services,omitempty"` // services that changed
	// Checked is every service considered. Reported because "already up to date" and "I could not
	// see any services" otherwise look identical, and the second is a silent failure that would
	// make a scheduled update a permanent no-op.
	Checked      []string `json:"checked,omitempty"`
	BackupOutput string   `json:"backup,omitempty"`
	Health       string   `json:"health,omitempty"`
}

// ErrNotAutoUpdatable is returned when --auto meets a stack that has not opted in.
var ErrNotAutoUpdatable = errors.New("stack is not marked autoUpdate")

// ErrNoHealthCheck is returned when an unattended update has no way to verify itself.
var ErrNoHealthCheck = errors.New("stack has no healthUrl, so an update cannot be verified or rolled back")

// Run performs the update. It returns a Result describing what happened; an error means the stack
// may need attention, and Result.RolledBack says whether it was put back first.
func Run(r Runner, o Options) (Result, error) {
	if o.Auto {
		// Both of these are refusals on purpose: an unattended run must be opted into, and must be
		// able to tell a healthy start from a crash loop.
		if !o.AutoUpdate {
			return Result{Skipped: "not marked autoUpdate"}, ErrNotAutoUpdatable
		}
		if !o.HasHealthCheck {
			return Result{Skipped: "no healthUrl"}, ErrNoHealthCheck
		}
	}

	if _, err := r.Pull(o.Services); err != nil {
		return Result{}, fmt.Errorf("pulling images: %w", err)
	}

	// Read state after pulling: what matters is whether each container is running what its reference
	// now points at, not what moved during this command.
	after, err := r.Images(o.Services)
	if err != nil {
		return Result{}, fmt.Errorf("reading images: %w", err)
	}
	before := after

	checked := serviceNames(after)
	if len(checked) == 0 {
		// No services with images means the query failed or the project is build-only. Either way,
		// silently reporting "up to date" would hide a scheduler that has been doing nothing for
		// weeks.
		return Result{Skipped: "no services with images found"}, errors.New("found no services with images to update — check the stack's dir and compose file")
	}

	changed := staleServices(after)
	if len(changed) == 0 {
		return Result{Skipped: "already up to date", Checked: checked}, nil
	}

	if o.DryRun {
		return Result{Skipped: "dry run", Services: changed, Checked: checked}, nil
	}

	res := Result{Services: changed, Checked: checked}

	// Back up before recreating, not after: if the new image migrates the schema on start, the
	// backup taken afterwards already contains the migration.
	if o.HasBackup && !o.SkipBackup {
		out, err := r.Backup()
		if err != nil {
			return res, fmt.Errorf("pre-update backup failed, not updating: %w", err)
		}
		res.BackupOutput = out
	}

	if _, err := r.Up(changed); err != nil {
		// Failing to start is exactly the case rollback exists for.
		return rollback(r, o, before, changed, res, fmt.Errorf("recreating containers: %w", err))
	}

	if !o.HasHealthCheck {
		// Nothing to verify against; an attended run is allowed to accept that.
		res.Updated = true
		res.Health = "not checked"
		return res, nil
	}

	ok, err := waitHealthy(r, o)
	if ok {
		res.Updated = true
		res.Health = "healthy"
		return res, nil
	}
	why := "health check did not pass"
	if err != nil {
		why = "health check errored: " + err.Error()
	}
	return rollback(r, o, before, changed, res, errors.New(why))
}

// rollback restores the images recorded before the update and brings the services back up.
func rollback(r Runner, o Options, before []compose.Image, changed []string, res Result, cause error) (Result, error) {
	res.RolledBack = true

	var failures []string
	for _, img := range before {
		if !contains(changed, img.Service) {
			continue
		}
		// Point the reference back at the image the container was running before this update. That
		// image is still on the host — untagged now, reachable only by id, which is why the id was
		// captured before anything was recreated.
		if img.Running == "" {
			failures = append(failures, img.Service+" (no previous image to restore)")
			continue
		}
		if _, err := r.Retag(img.Running, img.Ref); err != nil {
			failures = append(failures, img.Service+" (retag: "+err.Error()+")")
		}
	}
	if _, err := r.Up(changed); err != nil {
		failures = append(failures, "up: "+err.Error())
	}

	if len(failures) > 0 {
		return res, fmt.Errorf("%w; rollback incomplete: %s", cause, strings.Join(failures, "; "))
	}

	// Confirm the rollback actually restored service, so the report is not merely hopeful.
	if o.HasHealthCheck {
		if ok, _ := waitHealthy(r, o); ok {
			res.Health = "rolled back, healthy"
			return res, fmt.Errorf("%w; rolled back to the previous images", cause)
		}
		res.Health = "rolled back, still unhealthy"
		return res, fmt.Errorf("%w; rolled back but the stack is still not healthy", cause)
	}
	return res, fmt.Errorf("%w; rolled back to the previous images", cause)
}

func waitHealthy(r Runner, o Options) (bool, error) {
	attempts := o.HealthAttempts
	if attempts <= 0 {
		attempts = 20
	}
	delay := o.HealthDelay
	if delay <= 0 {
		delay = 3 * time.Second
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		ok, err := r.Healthy()
		if ok {
			return true, nil
		}
		lastErr = err
		if i < attempts-1 {
			time.Sleep(delay)
		}
	}
	return false, lastErr
}

// staleServices reports services whose container is running something other than what their
// reference now points at.
//
// An earlier version compared the tag's id before and after the pull, which only ever noticed
// changes the pull itself caused. It missed a tag moved by a local rebuild, and — worse — reported
// "already up to date" for a stack whose containers were running an image the tag no longer pointed
// to, which is exactly the state a half-finished deploy leaves behind.
func staleServices(imgs []compose.Image) []string {
	var stale []string
	for _, i := range imgs {
		if i.Stale() {
			stale = append(stale, i.Service)
		}
	}
	return stale
}

func serviceNames(imgs []compose.Image) []string {
	var names []string
	for _, i := range imgs {
		names = append(names, i.Service)
	}
	return names
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// SSHRunner is the real Runner: compose over SSH.
type SSHRunner struct {
	Client    *sshx.Client
	Stack     compose.Stack
	HealthURL string
	DoBackup  func() (string, error)
}

func (s SSHRunner) Images(svcs []string) ([]compose.Image, error) {
	return compose.Images(s.Client, s.Stack, svcs)
}

func (s SSHRunner) Pull(svcs []string) (string, error) {
	return compose.PullUpdatable(s.Client, s.Stack, svcs)
}

func (s SSHRunner) Up(svcs []string) (string, error) {
	return compose.Up(s.Client, s.Stack, compose.UpOptions{Services: svcs})
}

func (s SSHRunner) Retag(id, ref string) (string, error) {
	return compose.Retag(s.Client, s.Stack, id, ref)
}

func (s SSHRunner) Healthy() (bool, error) {
	if s.HealthURL == "" {
		return false, errors.New("no health url")
	}
	// Probed from the VM, so a stack bound to localhost is reachable and no port has to be exposed
	// publicly just to be checked.
	//
	// A container that is still starting — or has just died — refuses the connection, and curl exits
	// non-zero. That is the ordinary case while waiting, not a fault in the check, so it reports "not
	// healthy" rather than an error. Treating it as an error made a failed update read as though the
	// health check itself had broken.
	out, err := s.Client.Run("curl -s -o /dev/null -w '%{http_code}' --max-time 10 " + sshx.Quote(s.HealthURL))
	return healthyFromProbe(out, err), nil
}

// healthyFromProbe reads a curl probe.
//
// A container that is still starting — or has just died — refuses the connection and curl exits
// non-zero (7). That is the ordinary case while waiting, not a fault in the check. Reporting it as an
// error made a failed update read as though the health check itself had broken, which sent the reader
// looking in the wrong place.
func healthyFromProbe(out string, runErr error) bool {
	if runErr != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(out), "2")
}

func (s SSHRunner) Backup() (string, error) {
	if s.DoBackup == nil {
		return "", nil
	}
	return s.DoBackup()
}
