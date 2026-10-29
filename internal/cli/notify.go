// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BaryoDev/BaryoVM/internal/ui"
)

const (
	notifyFailure = "failure"
	notifyAlways  = "always"

	// notifyEnvVar lets the URL come from a CI secret instead of the command line, where it would
	// show up in process listings.
	notifyEnvVar = "BARYOVM_NOTIFY_URL"

	notifyTimeout  = 10 * time.Second
	notifyMaxBody  = 64 << 10
	notifyMaxError = 2 << 10
)

type notifyOptions struct {
	URL string
	On  string
}

// notifyResult is what the update envelope reports under data.notify. Target is scheme and host
// only: the URL itself usually carries a token.
type notifyResult struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// The seams: tests replace these. sendWebhook does one POST, notifyRetryDelay is the wait before
// the single retry.
var (
	sendWebhook      = postWebhook
	notifyRetryDelay = 2 * time.Second
)

// resolveNotify turns the flags and the environment into options, refusing a bad URL before any
// connection is made. The flag wins over the environment. The environment variable is ignored
// without --auto (a CI secret set for the whole job must not break an interactive-style update),
// while the flag without --auto is refused: it asks for something that would never happen.
func resolveNotify(flagSet bool, flagURL, on string, auto bool, getenv func(string) string) (notifyOptions, error) {
	if on != notifyFailure && on != notifyAlways {
		return notifyOptions{}, fmt.Errorf("--notify-on must be failure or always, not %q", on)
	}
	raw := flagURL
	if !flagSet {
		raw = getenv(notifyEnvVar)
	}
	if raw == "" {
		if flagSet {
			return notifyOptions{}, fmt.Errorf("--notify needs a URL")
		}
		return notifyOptions{On: on}, nil
	}
	if !auto {
		if flagSet {
			return notifyOptions{}, fmt.Errorf("--notify only applies to unattended runs: add --auto")
		}
		return notifyOptions{On: on}, nil
	}
	if _, err := checkNotifyURL(raw); err != nil {
		return notifyOptions{}, err
	}
	return notifyOptions{URL: raw, On: on}, nil
}

// checkNotifyURL accepts https, and http only for a loopback host (tests and local relays). It
// returns the redacted target, and its errors never contain the URL.
func checkNotifyURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("the notify URL is not a valid URL (it is not shown, it may hold a token)")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", fmt.Errorf("the notify URL must be https: http is only allowed for localhost (host %s)", u.Host)
		}
	default:
		return "", fmt.Errorf("the notify URL must be https, not %q", u.Scheme)
	}
	return redactURL(u), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func redactURL(u *url.URL) string { return u.Scheme + "://" + u.Host }

// notifyBody marshals the envelope, kept under notifyMaxBody. An oversized one loses its data and
// has its error cut, so the code and the verdict still arrive.
func notifyBody(env ui.Result) []byte {
	b, err := json.Marshal(env)
	if err == nil && len(b) <= notifyMaxBody {
		return b
	}
	env.Data = nil
	if len(env.Error) > notifyMaxError {
		env.Error = env.Error[:notifyMaxError] + "..."
	}
	b, _ = json.Marshal(env)
	return b
}

// shouldNotify is the policy: failures always, successes only when asked for.
func shouldNotify(on string, ok bool) bool { return on == notifyAlways || !ok }

// sendNotify posts the envelope, retrying once, and never returns the URL in its result.
func sendNotify(raw string, env ui.Result) *notifyResult {
	target, err := checkNotifyURL(raw)
	res := &notifyResult{Target: target}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	body := notifyBody(env)
	err = sendWebhook(raw, body)
	if err != nil && retryable(err) {
		time.Sleep(notifyRetryDelay)
		err = sendWebhook(raw, body)
	}
	if err != nil {
		res.Error = scrub(err.Error(), raw)
		return res
	}
	res.OK = true
	return res
}

type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("the endpoint answered HTTP %d", e.code) }

func retryable(err error) bool {
	var se statusError
	if errors.As(err, &se) {
		return se.code >= 500
	}
	return true
}

// scrub removes the URL, however the HTTP stack chose to quote it.
func scrub(msg, raw string) string {
	msg = strings.ReplaceAll(msg, raw, "<url>")
	if u, err := url.Parse(raw); err == nil {
		if p := u.EscapedPath(); len(p) > 1 {
			msg = strings.ReplaceAll(msg, p, "<path>")
		}
		if u.RawQuery != "" {
			msg = strings.ReplaceAll(msg, u.RawQuery, "<query>")
		}
	}
	return msg
}

func postWebhook(raw string, body []byte) error {
	client := &http.Client{
		Timeout: notifyTimeout,
		// A redirect would carry the token somewhere the operator did not choose.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequest(http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not build the request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "baryovm/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError{resp.StatusCode}
	}
	return nil
}
