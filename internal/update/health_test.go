package update

import (
	"errors"
	"testing"
)

// The health probe runs curl on the VM. A container that is still starting, or has just died,
// refuses the connection, and curl exits non-zero (7), which surfaces as an error from the SSH run.
//
// Treating that as an error rather than as "not healthy yet" made a failed update report
// "health check errored", which reads as a broken check and sends whoever is looking at it to the
// wrong place. It is the ordinary case while waiting for a container to come up.
func TestHealthProbeReadsCurlOutput(t *testing.T) {
	cases := []struct {
		name string
		out  string
		err  error
		want bool
	}{
		{"200 is healthy", "200", nil, true},
		{"204 is healthy", "204", nil, true},
		{"trailing newline is tolerated", "200\n", nil, true},
		{"404 is not healthy", "404", nil, false},
		{"500 is not healthy", "500", nil, false},
		{"401 is not healthy", "401", nil, false},
		{"connection refused is not healthy, not an error", "", errors.New("exit status 7"), false},
		{"no output is not healthy", "", nil, false},
		{"curl's zero-code placeholder is not healthy", "000", nil, false},
	}
	for _, c := range cases {
		if got := healthyFromProbe(c.out, c.err); got != c.want {
			t.Errorf("%s: healthyFromProbe(%q, %v) = %v, want %v", c.name, c.out, c.err, got, c.want)
		}
	}
}
