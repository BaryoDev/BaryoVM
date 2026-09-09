// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Count is one label with a number, used for the top-N lists.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Login is one successful authentication, which is the part of this report worth
// reading closely: a stranger in this list matters more than any number below it.
type Login struct {
	User   string `json:"user"`
	Source string `json:"source"`
	Method string `json:"method"`
	Key    string `json:"key,omitempty"`
	Count  int    `json:"count"`
}

// Threats is what a VM is currently being hit with.
type Threats struct {
	Since string `json:"since"`
	// Window is what the journal could actually cover, which may be shorter than
	// Since if the machine rebooted. Reporting a 7-day count from 3 hours of logs
	// is the kind of quiet wrongness worth naming in the output.
	Window          string   `json:"window"`
	FailedTotal     int      `json:"failedTotal"`
	InvalidUser     int      `json:"invalidUser"`
	UniqueSources   int      `json:"uniqueSources"`
	AcceptedTotal   int      `json:"acceptedTotal"`
	PasswordAccepts int      `json:"passwordAccepts"`
	TopSources      []Count  `json:"topSources"`
	TopUsernames    []Count  `json:"topUsernames"`
	Logins          []Login  `json:"logins"`
	BannedNow       []string `json:"bannedNow"`
	BannedTotal     int      `json:"bannedTotal"`
	Fail2ban        string   `json:"fail2ban"`
}

// TargetedRealAccounts reports whether any failure named an account that actually
// exists. Generic dictionary traffic never does, so this is the line between
// background noise and someone who has learned something about the machine.
func (t Threats) TargetedRealAccounts(real []string) []string {
	var hits []string
	for _, u := range t.TopUsernames {
		for _, r := range real {
			if strings.EqualFold(u.Name, r) {
				hits = append(hits, u.Name)
			}
		}
	}
	return hits
}

func threatScript(since string) string {
	return `set -u
SINCE=` + sshx.Quote(since) + `
LOG=$(mktemp)
# -u sshd and the sshd-session comm, because OpenSSH 9.8+ splits per-connection work
# into sshd-session processes and a plain unit filter misses most auth lines.
sudo journalctl -u sshd -u ssh --since "$SINCE" --no-pager 2>/dev/null > "$LOG" || true

echo "WINDOW|$(head -1 "$LOG" | cut -c1-15)"
echo "FAILED|$(grep -cE 'Failed (password|publickey)|Invalid user|Connection closed by authenticating' "$LOG" || true)"
echo "INVALID|$(grep -c 'Invalid user' "$LOG" || true)"
echo "ACCEPTED|$(grep -c 'Accepted ' "$LOG" || true)"
echo "PASSWORDACCEPT|$(grep -c 'Accepted password' "$LOG" || true)"
echo "UNIQUE|$(grep -oE 'from [0-9a-fA-F:.]+' "$LOG" | sort -u | wc -l)"

grep -oE 'Invalid user [^ ]+' "$LOG" | awk '{print $3}' | sort | uniq -c | sort -rn | head -10 \
  | while read -r n u; do echo "USER|$u|$n"; done

# Accepted lines are excluded from the source ranking on purpose: the owner's own
# address would otherwise sit at the top of a list of attackers.
grep -E 'Failed |Invalid user|Connection closed by authenticating' "$LOG" \
  | grep -oE 'from [0-9a-fA-F:.]+' | awk '{print $2}' | sort | uniq -c | sort -rn | head -10 \
  | while read -r n ip; do echo "SRC|$ip|$n"; done

grep 'Accepted ' "$LOG" \
  | sed -E 's/.*Accepted ([a-z]+) for ([^ ]+) from ([0-9a-fA-F:.]+).*/\1 \2 \3/' \
  | sort | uniq -c | sort -rn | head -10 \
  | while read -r n method user ip; do echo "LOGIN|$user|$ip|$method|$n"; done
rm -f "$LOG"

if command -v fail2ban-client >/dev/null 2>&1 && systemctl is-active --quiet fail2ban 2>/dev/null; then
  echo "F2B|active"
  S=$(sudo fail2ban-client status sshd 2>/dev/null || true)
  echo "BANNEDTOTAL|$(echo "$S" | grep -oE 'Total banned:[[:space:]]*[0-9]+' | grep -oE '[0-9]+' || true)"
  echo "BANNEDNOW|$(echo "$S" | sed -n 's/.*Banned IP list:[[:space:]]*//p')"
else
  echo "F2B|inactive"
fi
`
}

// Collect reads what the machine has been hit with since the given systemd
// timestamp, for example "24 hours ago" or "2026-09-01".
func Collect(c *sshx.Client, since string) (Threats, error) {
	out, err := c.Run(threatScript(since))
	if err != nil {
		return Threats{}, fmt.Errorf("threats: %w", err)
	}
	t := parseThreats(out)
	t.Since = since
	return t, nil
}

// parseThreats is pure so the shape of the report is testable without a VM.
func parseThreats(out string) Threats {
	var t Threats
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimRight(line, "\r"), "|")
		if len(p) < 2 {
			continue
		}
		switch p[0] {
		case "WINDOW":
			t.Window = strings.TrimSpace(p[1])
		case "FAILED":
			t.FailedTotal = atoi(p[1])
		case "INVALID":
			t.InvalidUser = atoi(p[1])
		case "ACCEPTED":
			t.AcceptedTotal = atoi(p[1])
		case "PASSWORDACCEPT":
			t.PasswordAccepts = atoi(p[1])
		case "UNIQUE":
			t.UniqueSources = atoi(p[1])
		case "F2B":
			t.Fail2ban = p[1]
		case "BANNEDTOTAL":
			t.BannedTotal = atoi(p[1])
		case "BANNEDNOW":
			for _, ip := range strings.Fields(p[1]) {
				t.BannedNow = append(t.BannedNow, ip)
			}
		case "USER":
			if len(p) >= 3 {
				t.TopUsernames = append(t.TopUsernames, Count{Name: p[1], Count: atoi(p[2])})
			}
		case "SRC":
			if len(p) >= 3 {
				t.TopSources = append(t.TopSources, Count{Name: p[1], Count: atoi(p[2])})
			}
		case "LOGIN":
			if len(p) >= 5 {
				t.Logins = append(t.Logins, Login{User: p[1], Source: p[2], Method: p[3], Count: atoi(p[4])})
			}
		}
	}
	sort.SliceStable(t.TopSources, func(i, j int) bool { return t.TopSources[i].Count > t.TopSources[j].Count })
	sort.SliceStable(t.TopUsernames, func(i, j int) bool { return t.TopUsernames[i].Count > t.TopUsernames[j].Count })
	return t
}
