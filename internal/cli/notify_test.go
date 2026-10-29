// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/fleet"
	"github.com/BaryoDev/BaryoVM/internal/ui"
)

func noRetryWait(t *testing.T) {
	old := notifyRetryDelay
	notifyRetryDelay = 0
	t.Cleanup(func() { notifyRetryDelay = old })
}

// A stack marked autoUpdate with no health check is refused before any connection, which is a
// failed unattended run the operator must hear about.
func refusedStack(t *testing.T) {
	t.Setenv("BARYOVM_HOME", t.TempDir())
	s := &fleet.Store{}
	s.Upsert(fleet.VM{Name: "oracle", Host: "127.0.0.1", Port: 1, User: "deploy", KeyPath: "/nonexistent/key"})
	s.UpsertStack(fleet.Stack{Name: "barako", VM: "oracle", Dir: "/opt/barako", AutoUpdate: true})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedAutoRunPostsTheEnvelope(t *testing.T) {
	noRetryWait(t)
	refusedStack(t)
	var got ui.Result
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
	}))
	defer srv.Close()

	err := runStackUpdateNotify("barako", nil, true, false, false, 1, time.Millisecond,
		notifyOptions{URL: srv.URL + "/hook/secret-token", On: notifyFailure})

	if err == nil {
		t.Fatal("the refusal must still be the result")
	}
	if atomic.LoadInt32(&hits) != 1 || got.OK || got.Action != "stack update" || got.Error == "" {
		t.Fatalf("want one posted failure envelope, got hits=%d %+v", hits, got)
	}
}

func TestSuccessDoesNotPostByDefault(t *testing.T) {
	if shouldNotify(notifyFailure, true) {
		t.Fatal("a success must not post by default")
	}
	if !shouldNotify(notifyFailure, false) || !shouldNotify(notifyAlways, true) || !shouldNotify(notifyAlways, false) {
		t.Fatal("failures always post, and always posts everything")
	}
}

func TestNotifyErrorSurfacesAndKeepsTheUpdateError(t *testing.T) {
	noRetryWait(t)
	refusedStack(t)
	var calls int32
	old := sendWebhook
	sendWebhook = func(raw string, body []byte) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("connection refused")
	}
	t.Cleanup(func() { sendWebhook = old })

	err := runStackUpdateNotify("barako", nil, true, false, false, 1, time.Millisecond,
		notifyOptions{URL: "https://hooks.example.com/T0/secret", On: notifyFailure})

	if err == nil || !strings.Contains(err.Error(), "healthUrl") {
		t.Fatalf("the update's own error must survive, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("want one try and one retry, got %d", calls)
	}
	res := sendNotify("https://hooks.example.com/T0/secret", ui.Result{Action: "stack update"})
	if res.OK || res.Error == "" {
		t.Fatalf("a notify failure must be reported: %+v", res)
	}
}

func TestBrokenEndpointIsReported(t *testing.T) {
	noRetryWait(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	res := sendNotify(srv.URL+"/x", ui.Result{OK: true, Action: "stack update"})
	if res.OK || !strings.Contains(res.Error, "500") {
		t.Fatalf("want a reported 500, got %+v", res)
	}
}

func TestNotifyURLIsNeverPrinted(t *testing.T) {
	noRetryWait(t)
	const secret = "s3cr3t-token"
	raw := "https://hooks.example.invalid/services/" + secret + "?key=" + secret
	old := sendWebhook
	sendWebhook = func(u string, body []byte) error { return errors.New("Post \"" + u + "\": dial failed") }
	t.Cleanup(func() { sendWebhook = old })

	res := sendNotify(raw, ui.Result{Action: "stack update"})

	b, _ := json.Marshal(res)
	if strings.Contains(string(b), secret) || res.Target != "https://hooks.example.invalid" {
		t.Fatalf("the URL leaked or the target is wrong: %s", b)
	}
	if _, err := checkNotifyURL("http://hooks.example.invalid/" + secret); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("a refused URL must not echo its path: %v", err)
	}
}

func TestNotifyURLRules(t *testing.T) {
	for _, ok := range []string{"https://a.example/x", "http://localhost:9/x", "http://127.0.0.1:9/x", "http://[::1]:9/x"} {
		if _, err := checkNotifyURL(ok); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://a.example/x", "ftp://a.example/x", "not a url", "https://"} {
		if _, err := checkNotifyURL(bad); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
}

func TestResolveNotify(t *testing.T) {
	env := func(k string) string {
		if k == notifyEnvVar {
			return "https://hooks.example.com/e"
		}
		return ""
	}
	o, err := resolveNotify(false, "", notifyFailure, true, env)
	if err != nil || o.URL != "https://hooks.example.com/e" {
		t.Fatalf("env should supply the URL: %+v %v", o, err)
	}
	o, _ = resolveNotify(true, "https://flag.example.com/f", notifyFailure, true, env)
	if o.URL != "https://flag.example.com/f" {
		t.Fatalf("flag beats env: %+v", o)
	}
	if o, err = resolveNotify(false, "", notifyFailure, false, env); err != nil || o.URL != "" {
		t.Fatalf("env without --auto is ignored: %+v %v", o, err)
	}
	if _, err = resolveNotify(true, "https://x.example.com", notifyFailure, false, env); err == nil {
		t.Fatal("--notify without --auto must be refused")
	}
	if _, err = resolveNotify(false, "", "sometimes", true, env); err == nil {
		t.Fatal("bad --notify-on must be refused")
	}
	if _, err = resolveNotify(true, "http://example.com/x", notifyFailure, true, env); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
}

func TestOversizedEnvelopeIsBounded(t *testing.T) {
	b := notifyBody(ui.Result{Action: "stack update", Error: strings.Repeat("x", 200<<10), Data: strings.Repeat("y", 200<<10)})
	if len(b) > notifyMaxBody {
		t.Fatalf("body is %d bytes", len(b))
	}
}
