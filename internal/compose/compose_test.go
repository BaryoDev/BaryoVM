package compose

import "testing"

// The image reference has to come from `compose config`, because that is the only place it appears
// as a name. `compose ps` and `compose images` report what the container actually runs, which on a
// host that resolved the tag to a digest is a bare sha256, which is no use as a rollback target, since
// `docker tag` needs a name. Getting this wrong is quiet: no reference means no detected change,
// which reads as "already up to date" forever.
func TestParseConfigImages(t *testing.T) {
	const out = `{
      "services": {
        "app":       {"image": "ghcr.io/baryodev/barako-cms:playground"},
        "admin":     {"image": "ghcr.io/baryodev/barako-admin:playground"},
        "postgres":  {"image": "postgres:16-alpine"},
        "worker":    {"build": {"context": "."}}
      }
    }`

	refs, err := parseConfigImages(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := refs["app"]; got != "ghcr.io/baryodev/barako-cms:playground" {
		t.Errorf("app: got %q", got)
	}
	if got := refs["postgres"]; got != "postgres:16-alpine" {
		t.Errorf("postgres: got %q", got)
	}
	// A build-only service has nothing to pull or roll back to, so it must not appear at all.
	if _, ok := refs["worker"]; ok {
		t.Errorf("build-only service should be omitted, got %q", refs["worker"])
	}
	if len(refs) != 3 {
		t.Errorf("expected 3 services with images, got %d: %v", len(refs), refs)
	}
}

func TestParseConfigImagesRejectsGarbage(t *testing.T) {
	// A compose version that prints something unexpected must be an error, not an empty map that
	// silently means "nothing to update".
	if _, err := parseConfigImages("not json"); err == nil {
		t.Fatal("expected an error for unparseable output")
	}
}

// Output that is only newlines is nothing read. Counting it as a line would report lines:1 with no
// note, which is the ambiguity this is meant to remove wearing a different hat.
func TestDescribeLogsTreatsBlankOutputAsNothingRead(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n\t\n"} {
		r := DescribeLogs(in)
		if r.Lines != 0 {
			t.Errorf("%q: expected 0 lines, got %d", in, r.Lines)
		}
		if r.Note == "" {
			t.Errorf("%q: expected a note saying the read found nothing", in)
		}
		if r.Output != in {
			t.Errorf("%q: output was not preserved, got %q", in, r.Output)
		}
	}

	r := DescribeLogs("one\n\nthree\n")
	if r.Lines != 2 {
		t.Errorf("blank lines should not count, got %d", r.Lines)
	}
	if r.Note != "" {
		t.Errorf("a non-empty read must not carry a note: %q", r.Note)
	}
}
