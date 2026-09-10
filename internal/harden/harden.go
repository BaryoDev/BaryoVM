// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package harden applies a small, opinionated SSH hardening policy to a VM over
// SSH, idempotently, and reads back what the machine is currently being hit with.
//
// The policy is deliberately narrow. It only touches things that cannot lock the
// owner out: it never changes the port, never disables public key authentication,
// and never edits authorized_keys. Every run validates the sshd configuration
// before reloading, and reloads rather than restarts, so established sessions
// survive even if something is wrong.
package harden

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Change is one thing the policy did, or would do under DryRun.
type Change struct {
	Item   string `json:"item"`
	Action string `json:"action"` // applied, already-correct, skipped, would-apply
	Detail string `json:"detail"`
}

// Report is the outcome of Apply.
type Report struct {
	OS            string   `json:"os"`
	SSHVersion    string   `json:"sshVersion"`
	PenaltiesUsed bool     `json:"penaltiesUsed"`
	Fail2ban      string   `json:"fail2ban"`
	DryRun        bool     `json:"dryRun"`
	Changes       []Change `json:"changes"`
}

// Options controls Apply.
type Options struct {
	// DryRun reports what would change and writes nothing.
	DryRun bool
	// IgnoreCIDRs are never banned. Loopback is always included.
	IgnoreCIDRs []string
}

const sshdDropIn = "/etc/ssh/sshd_config.d/10-baryovm-hardening.conf"

// script builds the remote shell. Written as one script rather than a sequence of
// round trips so a half-applied policy is not possible across a dropped connection.
func script(o Options) string {
	ignore := strings.Join(append([]string{"127.0.0.1/8", "::1"}, o.IgnoreCIDRs...), " ")
	dry := "0"
	if o.DryRun {
		dry = "1"
	}
	return `set -u
DRY=` + dry + `
IGNORE="` + ignore + `"
say() { echo "CHANGE|$1|$2|$3"; }

# Every privileged step below is sudo, and sudo that cannot run without a password
# does not fail loudly here: it fails quietly inside a probe and the policy then
# reports a machine as hardened having applied half of it. Refuse up front instead.
if ! sudo -n true 2>/dev/null; then
  echo "ERROR|non-interactive sudo is required on this host, and is not available for this user"
  exit 1
fi

# --- facts the policy branches on -------------------------------------------------
if [ -f /etc/os-release ]; then . /etc/os-release; OSID="${ID:-unknown}"; else OSID=unknown; fi
echo "FACT|os|$OSID"
SSHV=$(sshd -V 2>&1 | head -1 || true)
echo "FACT|sshVersion|$SSHV"

# PerSourcePenalties landed in OpenSSH 9.8. Asking the daemon is better than parsing a
# version string, because a distro may backport it.
#
# sudo is not optional here: sshd -T has to read the host keys, so as an ordinary user
# it fails and prints nothing, and the probe then reports "not supported" on a daemon
# that supports it perfectly well. That silently drops the most useful half of this
# policy, which is exactly the kind of wrong that looks like a clean run.
if sudo sshd -T 2>/dev/null | grep -qi '^persourcepenalties'; then PENALTIES=1; else PENALTIES=0; fi
echo "FACT|penalties|$PENALTIES"

# --- 1. sshd drop-in --------------------------------------------------------------
# sshd takes the FIRST value it sees for a keyword, and the stock drop-ins on RHEL
# (50-redhat.conf) set GSSAPIAuthentication and X11Forwarding. A file numbered above
# those is read later and silently loses, which is why this one is 10-.
# PermitRootLogin no would lock out the next login when root is the account BaryoVM
# itself connects as. The session in flight survives a reload, so the damage only shows
# up the next time somebody needs in, which is the worst time to find it. For a root
# connection the policy still hardens, to key-only, rather than to nothing.
WHOAMI=$(id -un)
if [ "$WHOAMI" = "root" ]; then ROOTLOGIN="prohibit-password"; else ROOTLOGIN="no"; fi

DROPIN=` + sshdDropIn + `
NEW=$(mktemp)
{
  echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
  echo "GSSAPIAuthentication no"
  echo "X11Forwarding no"
  echo "LoginGraceTime 30"
  echo "PermitRootLogin $ROOTLOGIN"
  echo "PasswordAuthentication no"
  echo "PubkeyAuthentication yes"
  if [ "$PENALTIES" = "1" ]; then
    echo "PerSourcePenaltyExemptList 127.0.0.1,::1"
    echo "PerSourcePenalties crash:180 authfail:40 noauth:20 grace-exceeded:60 refuseconnection:60 max:3600 min:60"
  fi
} > "$NEW"

# sudo test, not [ -f ]: /etc/ssh/sshd_config.d is mode 700 on RHEL-family images, so
# an unprivileged existence check answers "no" for a file that is plainly there, the
# comparison below never runs, and every run reports itself as having changed something.
if sudo test -f "$DROPIN" && sudo cmp -s "$NEW" "$DROPIN"; then
  say sshd already-correct "$DROPIN matches policy"
elif [ "$DRY" = "1" ]; then
  say sshd would-apply "$DROPIN would be written"
else
  # grep -q on the main file: if Include is missing the drop-in would be inert, and a
  # policy that is written but never read is the worst outcome available here.
  if ! sudo grep -q "^Include /etc/ssh/sshd_config.d/\*.conf" /etc/ssh/sshd_config; then
    echo "ERROR|sshd_config has no Include for sshd_config.d, refusing to write a file nothing reads"
    exit 1
  fi
  # Keep whatever was there. Deleting on failure would take the previous, working
  # policy with the rejected one, so a bad run would leave the host less hardened
  # than it was before it ran.
  PREV=""
  if sudo test -f "$DROPIN"; then PREV=$(mktemp); sudo cat "$DROPIN" > "$PREV"; fi
  sudo install -m 600 -o root -g root "$NEW" "$DROPIN"
  if sudo sshd -t 2>/dev/null; then
    sudo systemctl reload sshd 2>/dev/null || sudo systemctl reload ssh
    say sshd applied "wrote $DROPIN and reloaded"
    [ -n "$PREV" ] && rm -f "$PREV"
  else
    if [ -n "$PREV" ]; then
      sudo install -m 600 -o root -g root "$PREV" "$DROPIN"
      rm -f "$PREV"
      echo "ERROR|sshd rejected the new config, the previous drop-in was restored and nothing reloaded"
    else
      sudo rm -f "$DROPIN"
      echo "ERROR|sshd rejected the config, drop-in removed and nothing reloaded"
    fi
    exit 1
  fi
fi
rm -f "$NEW"

# --- 2. fail2ban ------------------------------------------------------------------
if command -v fail2ban-client >/dev/null 2>&1; then
  F2B_PRESENT=1
else
  F2B_PRESENT=0
fi

if [ "$F2B_PRESENT" = "0" ] && [ "$DRY" = "1" ]; then
  say fail2ban would-apply "would install fail2ban"
elif [ "$F2B_PRESENT" = "0" ]; then
  if command -v dnf >/dev/null 2>&1; then
    sudo dnf -y -q install fail2ban fail2ban-firewalld >/dev/null 2>&1 || sudo dnf -y -q install fail2ban >/dev/null 2>&1
  elif command -v apt-get >/dev/null 2>&1; then
    sudo DEBIAN_FRONTEND=noninteractive apt-get -qq update >/dev/null 2>&1
    sudo DEBIAN_FRONTEND=noninteractive apt-get -qq -y install fail2ban >/dev/null 2>&1
  fi
  if command -v fail2ban-client >/dev/null 2>&1; then
    say fail2ban applied "installed"
  else
    say fail2ban skipped "no supported package manager, or install failed"
  fi
else
  say fail2ban already-correct "already installed"
fi

# The jail only makes sense once the binary exists.
if command -v fail2ban-client >/dev/null 2>&1; then
  # firewalld and fail2ban both write firewall rules; when firewalld is running, go
  # through it rather than having two things edit the same tables. Otherwise leave
  # banaction at the distribution default, which is correct for that distribution.
  if systemctl is-active --quiet firewalld 2>/dev/null; then
    BANACTION="banaction = firewallcmd-rich-rules"
  else
    BANACTION="# banaction left at the distribution default (no firewalld running)"
  fi
  # port = ssh means 22. On a host that moved sshd, bans would be written for a port
  # nothing is listening on, and the jail would look healthy while protecting nothing.
  SSHPORT=$(sudo sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')
  [ -z "$SSHPORT" ] && SSHPORT=ssh

  JAIL=$(mktemp)
  {
    echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
    echo "[DEFAULT]"
    echo "ignoreip = $IGNORE"
    echo "backend = systemd"
    echo "$BANACTION"
    echo "findtime = 10m"
    echo "maxretry = 4"
    echo "bantime = 1h"
    echo "bantime.increment = true"
    echo "bantime.factor = 2"
    echo "bantime.maxtime = 1w"
    echo ""
    echo "[sshd]"
    echo "enabled = true"
    # normal, not aggressive: aggressive counts a connection closed during auth, which
    # a client offering several keys before the right one also produces.
    echo "mode = normal"
    echo "port = $SSHPORT"
  } > "$JAIL"

  if sudo test -f /etc/fail2ban/jail.local && sudo cmp -s "$JAIL" /etc/fail2ban/jail.local; then
    say jail already-correct "/etc/fail2ban/jail.local matches policy"
  elif [ "$DRY" = "1" ]; then
    say jail would-apply "/etc/fail2ban/jail.local would be written"
  else
    PREVJAIL=""
    if sudo test -f /etc/fail2ban/jail.local; then PREVJAIL=$(mktemp); sudo cat /etc/fail2ban/jail.local > "$PREVJAIL"; fi
    sudo install -m 644 -o root -g root "$JAIL" /etc/fail2ban/jail.local
    if sudo fail2ban-client -t >/dev/null 2>&1; then
      sudo systemctl enable --now fail2ban >/dev/null 2>&1
      sudo systemctl reload fail2ban >/dev/null 2>&1 || sudo systemctl restart fail2ban >/dev/null 2>&1
      say jail applied "wrote jail.local and started fail2ban"
      [ -n "$PREVJAIL" ] && rm -f "$PREVJAIL"
    else
      # Same reasoning as the sshd drop-in: a rejected jail must not cost the host the
      # jail it already had.
      if [ -n "$PREVJAIL" ]; then
        sudo install -m 644 -o root -g root "$PREVJAIL" /etc/fail2ban/jail.local
        rm -f "$PREVJAIL"
        echo "ERROR|fail2ban rejected the new jail, the previous jail.local was restored"
      else
        sudo rm -f /etc/fail2ban/jail.local
        echo "ERROR|fail2ban rejected the jail, jail.local removed"
      fi
      exit 1
    fi
  fi
  rm -f "$JAIL"
fi

if systemctl is-active --quiet fail2ban 2>/dev/null; then echo "FACT|fail2ban|active"; else echo "FACT|fail2ban|inactive"; fi
`
}

// Apply runs the policy. It is safe to run repeatedly: every step reports
// already-correct when the machine is already in the desired state.
func Apply(c *sshx.Client, o Options) (Report, error) {
	out, err := c.Run(script(o))
	r := parseApply(out)
	r.DryRun = o.DryRun
	if err != nil {
		return r, fmt.Errorf("harden: %w", err)
	}
	if msg := firstError(out); msg != "" {
		return r, fmt.Errorf("harden: %s", msg)
	}
	return r, nil
}

// parseApply turns the script's line protocol into a Report. Separated from the SSH
// call so the parsing has tests that do not need a machine.
func parseApply(out string) Report {
	var r Report
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimRight(line, "\r"), "|")
		switch {
		case len(parts) >= 3 && parts[0] == "FACT":
			switch parts[1] {
			case "os":
				r.OS = parts[2]
			case "sshVersion":
				r.SSHVersion = strings.Join(parts[2:], "|")
			case "penalties":
				r.PenaltiesUsed = parts[2] == "1"
			case "fail2ban":
				r.Fail2ban = parts[2]
			}
		case len(parts) >= 4 && parts[0] == "CHANGE":
			r.Changes = append(r.Changes, Change{Item: parts[1], Action: parts[2], Detail: strings.Join(parts[3:], "|")})
		}
	}
	return r
}

func firstError(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ERROR|") {
			return strings.TrimPrefix(strings.TrimRight(line, "\r"), "ERROR|")
		}
	}
	return ""
}

// atoi is a total function: anything unparseable counts as zero, because a missing
// number in a report should read as "none seen", not fail the whole command.
func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
