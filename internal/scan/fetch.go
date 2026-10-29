// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scan

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
)

// maxBody bounds what is read from any edge response.
const maxBody = 64 << 10

// FetchRequest is one GET.
type FetchRequest struct {
	URL string
	// SkipVerify turns certificate verification off. Only the bare-IP probe
	// sets it: the question there is whether the IP serves anything, and its
	// certificate cannot match an IP anyway.
	SkipVerify bool
}

// FetchResponse is what the edge checks read from a reply.
type FetchResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// Fetcher is the HTTP seam. Tests satisfy it with canned replies.
type Fetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error)
}

// HTTPFetcher is the real Fetcher. It never follows a redirect (a redirect is
// an answer, not a reason to send a second request), ignores proxy settings in
// the environment so a bare-IP probe goes where it says, and reads a bounded
// body.
type HTTPFetcher struct{}

func (HTTPFetcher) Fetch(ctx context.Context, fr FetchRequest) (FetchResponse, error) {
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Control: guardDial}).DialContext,
		// A bare-IP URL sends no SNI (crypto/tls omits it for IP hosts).
		TLSClientConfig: &tls.Config{InsecureSkipVerify: fr.SkipVerify}, // #nosec G402 -- opt-in per request, see FetchRequest.SkipVerify
	}
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fr.URL, nil)
	if err != nil {
		return FetchResponse{}, err
	}
	req.Header.Set("User-Agent", "baryovm-scan")
	resp, err := c.Do(req)
	if err != nil {
		return FetchResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return FetchResponse{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}
