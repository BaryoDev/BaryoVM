// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reported bug: `stack logs` on a container that writes to a file instead of stdout answered
// {"output": ""}, which is the same answer it gives for the wrong container name, the wrong host,
// or a Docker it never reached. The two have to read differently.
func TestEmptyLogsDoNotSerializeLikeAReadThatFailed(t *testing.T) {
	data, note := describeLogs("")

	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if got == `{"output":""}` {
		t.Fatalf("an empty log still serializes as a bare empty output: %s", got)
	}
	if !strings.Contains(got, `"lines":0`) {
		t.Errorf("no line count to say the read happened and found nothing: %s", got)
	}
	if !strings.Contains(got, `"note":`) {
		t.Errorf("no note saying what an empty log means: %s", got)
	}
	if note == "" {
		t.Error("nothing to print in human mode, where the output is also blank")
	}
}

// The same shape has to carry real logs, and must not claim an empty read when there was one.
func TestLogsReportTheirLineCount(t *testing.T) {
	data, note := describeLogs("app-1  | started\napp-1  | listening on 8080\n")

	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, `"lines":2`) {
		t.Errorf("expected 2 lines, got: %s", got)
	}
	if strings.Contains(got, `"note":`) {
		t.Errorf("a read with logs in it must not carry the empty-read note: %s", got)
	}
	if note != "" {
		t.Errorf("human mode prints the logs, so there is nothing to note: %q", note)
	}
}
