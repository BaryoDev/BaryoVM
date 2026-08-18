package compose

import (
	"strings"
	"testing"
)

// Regression cover for bugs found by running this against a real host. Each one passed its unit
// tests and its code review, and failed only against an actual Docker daemon, which is the most
// expensive place to find out, so they get pinned here.

// The image reference has to come from `compose config`. `ps` and `compose images` report what the
// container is running, which on a host that resolved a tag to a digest is a bare sha256 with a
// Repository of literally "sha256", which is no use as a rollback target, since `docker tag` needs a name.
// The first version read `ps`, so rollback would have failed at exactly the moment it was needed.
func TestImageReferencesComeFromConfigNotPs(t *testing.T) {
	s := Stack{Dir: "/opt/app"}

	cmd := s.ConfigCmd()
	if !strings.Contains(cmd, "config") {
		t.Fatalf("references must be read from `compose config`, got %q", cmd)
	}
	if strings.Contains(cmd, " ps") || strings.Contains(cmd, "images") {
		t.Fatalf("references must not come from ps/images, got %q", cmd)
	}
}

// A stack mixing registry images with locally built ones (BaryoClub runs postgres from Docker Hub
// beside its own api and web) cannot be pulled wholesale: compose fails the entire command on the
// first image with no registry behind it, which made such stacks permanently un-updatable.
func TestUpdatePullToleratesLocallyBuiltImages(t *testing.T) {
	s := Stack{Dir: "/opt/app"}

	if !strings.Contains(s.PullUpdatableCmd(nil), "--ignore-pull-failures") {
		t.Errorf("the update pull must skip unpullable images, got %q", s.PullUpdatableCmd(nil))
	}
	// The plain pull keeps failing loudly: an explicit `stack pull` should report a broken image
	// rather than quietly skipping it.
	if strings.Contains(s.PullCmd(nil), "--ignore-pull-failures") {
		t.Errorf("an explicit pull must not hide failures, got %q", s.PullCmd(nil))
	}
}

// Staleness is deployed-versus-declared, not what-moved-during-this-command. The first version
// compared the tag's id before and after the pull, so it saw nothing when a tag had been moved by a
// local rebuild, and worse, reported "already up to date" for a stack whose containers were
// running an image the tag no longer pointed at, which is what a half-finished deploy leaves behind.
func TestStaleComparesDeployedAgainstDeclared(t *testing.T) {
	cases := []struct {
		name              string
		running, declared string
		want              bool
	}{
		{"container matches the tag", "sha256:aaa", "sha256:aaa", false},
		{"tag moved ahead of the container", "sha256:aaa", "sha256:bbb", true},
		{"container running something newer than the tag", "sha256:bbb", "sha256:aaa", true},
		{"nothing running yet", "", "sha256:aaa", false},
		{"reference resolves to nothing locally", "sha256:aaa", "", false},
	}
	for _, c := range cases {
		got := Image{Service: "app", Ref: "repo:tag", Running: c.running, ID: c.declared}.Stale()
		if got != c.want {
			t.Errorf("%s: Stale()=%v, want %v", c.name, got, c.want)
		}
	}
}

// A service with no image (build-only) has nothing to pull or roll back to, so it must not appear at
// all. Otherwise it would be counted as a service that could not be resolved.
func TestBuildOnlyServicesAreExcluded(t *testing.T) {
	refs, err := parseConfigImages(`{"services":{"app":{"image":"repo:tag"},"worker":{"build":{"context":"."}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs["worker"]; ok {
		t.Errorf("build-only service should be omitted, got %v", refs)
	}
	if refs["app"] != "repo:tag" {
		t.Errorf("app: got %q", refs["app"])
	}
}
