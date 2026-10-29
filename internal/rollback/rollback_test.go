// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rollback

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/backup"
	"github.com/BaryoDev/BaryoVM/internal/compose"
)

type rec struct {
	seen []string
	out  string
}

func (r *rec) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	return r.out, nil
}

func TestFromComposeSkipsWhatHasNothingToGoBackTo(t *testing.T) {
	got := FromCompose("app", "release", []compose.Image{
		{Service: "web", Ref: "app-web:1", Running: "sha256:aaa"},
		{Service: "job", Ref: "app-job:1", Running: ""},
		{Service: "db", Ref: "", Running: "sha256:bbb"},
	})
	if len(got.Images) != 1 || got.Images[0].Service != "web" || got.Images[0].ID != "sha256:aaa" {
		t.Errorf("images: %+v", got.Images)
	}
	if got.Source != "release" || got.Stack != "app" || got.At == "" {
		t.Errorf("record: %+v", got)
	}
}

func TestSaveWritesIntoTheBackupDirAndLoadReadsItBack(t *testing.T) {
	cfg := backup.Config{}
	r := &rec{}
	if err := Save(r, cfg, FromCompose("app", "update", nil)); err != nil {
		t.Fatal(err)
	}
	if len(r.seen) != 1 || !strings.Contains(r.seen[0], File) || !strings.Contains(r.seen[0], "base64 -d") {
		t.Errorf("save command: %v", r.seen)
	}
	r.out = ""
	if got, err := Load(r, cfg); err != nil || got != nil {
		t.Errorf("empty should be nil: %v %v", got, err)
	}
	r.out = `{"stack":"app","at":"t","source":"update","images":[{"service":"web","ref":"r","id":"i"}]}`
	got, err := Load(r, cfg)
	if err != nil || got == nil || got.Images[0].ID != "i" {
		t.Errorf("load: %v %v", got, err)
	}
	r.out = "{"
	if _, err := Load(r, cfg); err == nil {
		t.Error("bad JSON should error")
	}
}

func TestApplyRetagsThenUpsAndNeverTouchesTheDatabase(t *testing.T) {
	r := &rec{}
	err := Apply(r, compose.Stack{Dir: "/opt/app"}, &Record{Images: []Image{{Service: "web", Ref: "app-web:1", ID: "sha256:aaa"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.seen) != 3 || !strings.Contains(r.seen[0], "image inspect") || !strings.Contains(r.seen[1], " tag ") || !strings.Contains(r.seen[2], "up") {
		t.Fatalf("want inspect, retag, then up, got %v", r.seen)
	}
	for _, c := range r.seen {
		if strings.Contains(c, "pg_restore") || strings.Contains(c, "psql") {
			t.Errorf("touched the database: %s", c)
		}
	}
}

func TestApplyWithNoImagesErrors(t *testing.T) {
	r := &rec{}
	if err := Apply(r, compose.Stack{Dir: "/opt/app"}, &Record{}); err == nil {
		t.Error("want an error")
	}
	if len(r.seen) != 0 {
		t.Errorf("ran %v", r.seen)
	}
}

type missingRunner struct {
	seen    []string
	missing string
}

func (r *missingRunner) Run(cmd string) (string, error) {
	r.seen = append(r.seen, cmd)
	if strings.Contains(cmd, "image inspect") && strings.Contains(cmd, r.missing) {
		return "", errors.New("no such image")
	}
	return "", nil
}

func TestApplyChangesNothingWhenAnImageIsMissing(t *testing.T) {
	r := &missingRunner{missing: "sha256:bbb"}
	rec := &Record{Images: []Image{
		{Service: "web", Ref: "app-web:1", ID: "sha256:aaa"},
		{Service: "worker", Ref: "app-worker:1", ID: "sha256:bbb"},
	}}
	err := Apply(r, compose.Stack{Dir: "/opt/app"}, rec)
	if err == nil || !strings.Contains(err.Error(), "sha256:bbb") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("want an error naming the image and saying nothing changed, got %v", err)
	}
	for _, c := range r.seen {
		if strings.Contains(c, " tag ") || strings.Contains(c, " up") {
			t.Errorf("changed something before the check passed: %s", c)
		}
	}
}

// store is a host holding one record file.
type store struct{ raw string }

func (s *store) Run(cmd string) (string, error) {
	if m := b64.FindStringSubmatch(cmd); m != nil {
		b, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			return "", err
		}
		s.raw = string(b)
		return "", nil
	}
	return s.raw, nil
}

var b64 = regexp.MustCompile(`printf %s '([A-Za-z0-9+/=]+)'`)

func TestAProtectedRecordIsNotReplaced(t *testing.T) {
	cfg := backup.Config{}
	s := &store{}
	if kept, err := SaveUnlessProtected(s, cfg, Record{Stack: "app", Images: []Image{{ID: "good"}}}); err != nil || kept {
		t.Fatalf("first save: kept=%v err=%v", kept, err)
	}
	if ok, err := SetProtected(s, cfg, true); err != nil || !ok {
		t.Fatalf("protect: %v %v", ok, err)
	}
	kept, err := SaveUnlessProtected(s, cfg, Record{Stack: "app", Images: []Image{{ID: "half"}}})
	if err != nil || !kept {
		t.Fatalf("protected save: kept=%v err=%v", kept, err)
	}
	if got, _ := Load(s, cfg); got.Images[0].ID != "good" || !got.Protected {
		t.Errorf("record was replaced: %+v", got)
	}
	if _, err := SetProtected(s, cfg, false); err != nil {
		t.Fatal(err)
	}
	if kept, err := SaveUnlessProtected(s, cfg, Record{Stack: "app", Images: []Image{{ID: "next"}}}); err != nil || kept {
		t.Fatalf("unprotected save: kept=%v err=%v", kept, err)
	}
	if got, _ := Load(s, cfg); got.Images[0].ID != "next" {
		t.Errorf("record: %+v", got)
	}
}

func TestSetProtectedWithNoRecordWritesNothing(t *testing.T) {
	r := &rec{}
	if ok, err := SetProtected(r, backup.Config{}, true); err != nil || ok || len(r.seen) != 1 {
		t.Errorf("ok=%v err=%v seen=%v", ok, err, r.seen)
	}
}
