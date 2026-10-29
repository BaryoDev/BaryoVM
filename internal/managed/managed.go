// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package managed builds the calls BaryoVM makes to the AWS CLI and the Azure CLI on the
// operator's machine, for the managed database that sits beside a stack's VM. It builds argv
// slices and checks the names that go into them. It never runs anything: the cli layer owns
// the exec seam, so the engine stays testable with no cloud account.
//
// Nothing here holds a credential. A Spec names a profile or a subscription; the CLI's own
// sign-in does the rest.
package managed

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Provider is which cloud CLI a stack uses.
type Provider string

const (
	AWS   Provider = "aws"
	Azure Provider = "azure"
)

// Spec is the managed database a stack names, and which CLI profile or subscription reaches it.
// Only names live here, never a key, token or password.
type Spec struct {
	Provider Provider `json:"provider"`

	// Profile and Region are for aws. Profile may be empty (the CLI's default profile).
	Profile string `json:"profile,omitempty"`
	Region  string `json:"region,omitempty"`

	// Subscription and ResourceGroup are for azure.
	Subscription  string `json:"subscription,omitempty"`
	ResourceGroup string `json:"resourceGroup,omitempty"`

	// Database is the RDS instance identifier or the flexible server name.
	Database string `json:"database"`
}

// Call is one CLI call as it ran: the argv, the exit code and what it printed. It goes into the
// -o json envelope. Output is cut to MaxOutput bytes.
type Call struct {
	Argv     []string `json:"argv"`
	ExitCode int      `json:"exitCode"`
	Output   string   `json:"output,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// MaxOutput is how much of one call's output is kept.
const MaxOutput = 4096

// Trim cuts s to MaxOutput bytes, keeping the end, which is where a CLI puts its error.
func Trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= MaxOutput {
		return s
	}
	return "..." + s[len(s)-MaxOutput:]
}

var (
	// nameRe is the shape every identifier must have. It starts with a letter or digit, which is
	// also what keeps a value from reading as a flag.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// awsDBRe is an RDS instance identifier: a letter first, then letters, digits and hyphens.
	awsDBRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)
	regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
)

func check(field, v string, re *regexp.Regexp, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if !re.MatchString(v) {
		return fmt.Errorf("%s %q is not allowed: letters, digits, '.', '_' and '-' only, starting with a letter or digit", field, v)
	}
	return nil
}

// Validate refuses a spec with a missing field, a field that belongs to the other provider, or
// a value outside the allowed characters. It is the injection boundary for every argv below.
func (s Spec) Validate() error {
	switch s.Provider {
	case AWS:
		if s.Subscription != "" || s.ResourceGroup != "" {
			return fmt.Errorf("subscription and resource group are for azure, not aws")
		}
		if err := check("profile", s.Profile, nameRe, false); err != nil {
			return err
		}
		if s.Region == "" {
			return fmt.Errorf("region is required for aws")
		}
		if !regionRe.MatchString(s.Region) {
			return fmt.Errorf("region %q is not allowed: use a region name such as ap-southeast-1", s.Region)
		}
		if s.Database == "" {
			return fmt.Errorf("database is required")
		}
		if !awsDBRe.MatchString(s.Database) {
			return fmt.Errorf("database %q is not allowed: an RDS identifier is letters, digits and '-', starting with a letter", s.Database)
		}
		return nil
	case Azure:
		if s.Profile != "" || s.Region != "" {
			return fmt.Errorf("profile and region are for aws, not azure")
		}
		if err := check("subscription", s.Subscription, nameRe, true); err != nil {
			return err
		}
		if err := check("resource group", s.ResourceGroup, nameRe, true); err != nil {
			return err
		}
		return check("database", s.Database, nameRe, true)
	}
	return fmt.Errorf("provider must be aws or azure, got %q", s.Provider)
}

// CLI is the program a spec runs.
func (s Spec) CLI() string {
	if s.Provider == Azure {
		return "az"
	}
	return "aws"
}

// Label is a short human name for the spec: the profile or subscription it reaches the cloud by.
func (s Spec) Label() string {
	if s.Provider == Azure {
		return "azure subscription " + s.Subscription
	}
	if s.Profile == "" {
		return "aws default profile"
	}
	return "aws profile " + s.Profile
}

func (s Spec) awsBase() []string {
	a := []string{"aws"}
	if s.Profile != "" {
		a = append(a, "--profile", s.Profile)
	}
	return append(a, "--region", s.Region)
}

// SignInArgv is the call that proves the CLI is installed and signed in.
func (s Spec) SignInArgv() []string {
	if s.Provider == Azure {
		return []string{"az", "account", "show", "--subscription", s.Subscription}
	}
	return append(s.awsBase(), "sts", "get-caller-identity")
}

// SignInHint is the command to run when the CLI is missing or signed out.
func (s Spec) SignInHint() string {
	if s.Provider == Azure {
		return "run `az login`, then check `az account show --subscription " + s.Subscription + "` (install: https://aka.ms/installazurecli)"
	}
	if s.Profile != "" {
		return "run `aws sso login --profile " + s.Profile + "` (or `aws configure --profile " + s.Profile + "`); install the AWS CLI if `aws` is missing"
	}
	return "run `aws sso login` or `aws configure`; install the AWS CLI if `aws` is missing"
}

// SnapshotID names a pre-release snapshot. Letters, digits and single hyphens, which both RDS
// and Azure accept.
func SnapshotID(now time.Time) string {
	return "baryovm-" + now.UTC().Format("20060102-150405")
}

// SnapshotArgvs is the ordered calls that take a snapshot of the database and wait until it is
// usable. The sign-in check comes first, so a signed-out CLI stops the release with its own
// hint. On azure, `backup create` returns when the backup is done.
func (s Spec) SnapshotArgvs(id string) ([][]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if !nameRe.MatchString(id) {
		return nil, fmt.Errorf("snapshot id %q is not allowed", id)
	}
	calls := [][]string{s.SignInArgv()}
	if s.Provider == Azure {
		return append(calls, []string{"az", "postgres", "flexible-server", "backup", "create",
			"--subscription", s.Subscription, "--resource-group", s.ResourceGroup,
			"--name", s.Database, "--backup-name", id}), nil
	}
	base := s.awsBase()
	create := append(append([]string{}, base...), "rds", "create-db-snapshot",
		"--db-instance-identifier", s.Database, "--db-snapshot-identifier", id)
	wait := append(append([]string{}, base...), "rds", "wait", "db-snapshot-available",
		"--db-snapshot-identifier", id)
	return append(calls, create, wait), nil
}

// Join renders an argv for display. It is for humans and dry runs; nothing executes it.
func Join(argv []string) string { return strings.Join(argv, " ") }
