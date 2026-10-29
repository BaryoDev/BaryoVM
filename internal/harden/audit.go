// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"fmt"
	"net"
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// StateReport marks a finding the policy deliberately leaves to a person. It is
// neither a change nor a clean bill: the CLI counts these and says so.
const StateReport = "report"

// Post-quantum hybrid key exchanges, preferred first. OpenSSH 9.9 ships both
// names; 9.0 to 9.8 only have sntrup, which 9.0 to 9.4 also know under the
// @openssh.com name.
const pqKex = "mlkem768x25519-sha256 sntrup761x25519-sha512 sntrup761x25519-sha512@openssh.com"

// NormalizeCIDRs validates each entry and returns it in canonical form. It runs on
// the Go side so a typo never reaches a firewall command.
func NormalizeCIDRs(in []string) ([]string, error) {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("--ssh-from: %q is not a CIDR (use 203.0.113.7/32 for one address)", s)
		}
		out = append(out, n.String())
	}
	return out, nil
}

// sshFromGuard refuses, before anything is changed, a port 22 limit that would cut
// off the address this session comes from. The session in flight survives a
// firewall change, so the damage would only show on the next login.
func sshFromGuard(o Options) string {
	if len(o.SSHFrom) == 0 {
		return ""
	}
	return `
# --- guard: do not write a 22 rule that excludes this session ----------------------
SSHFROM=` + sshx.Quote(strings.Join(o.SSHFrom, " ")) + `
CLIENT="${SSH_CLIENT%% *}"
in_cidr() {
  awk -v ip="$1" -v cidr="$2" 'function n(s,  p){split(s,p,".");return ((p[1]*256+p[2])*256+p[3])*256+p[4]}
    BEGIN{split(cidr,c,"/"); b=2^(32-c[2]); exit !(int(n(ip)/b)==int(n(c[1])/b))}'
}
if [ -z "$CLIENT" ]; then
  echo "ERROR|cannot tell which address this SSH session comes from (SSH_CLIENT is empty), refusing to limit port 22"
  exit 1
fi
ALLOWED=0
for cidr in $SSHFROM; do
  case "$cidr" in
    *:*) case "$CLIENT" in *:*) [ "$cidr" = "::/0" ] && ALLOWED=1 ;; esac ;;
    *) case "$CLIENT" in *:*) ;; *) in_cidr "$CLIENT" "$cidr" && ALLOWED=1 ;; esac ;;
  esac
done
if [ "$ALLOWED" != "1" ]; then
  echo "ERROR|the --ssh-from ranges do not include $CLIENT, the address this session comes from; refusing to write a 22 rule that would lock you out"
  exit 1
fi
`
}

// kexPrep decides whether the drop-in needs a KexAlgorithms line. It only puts the
// hybrid algorithms in front of the list the daemon already uses, so no client that
// connects today is locked out.
const kexPrep = `
# --- post-quantum key exchange: facts ---------------------------------------------
# ssh -Q lists what this OpenSSH build can do. On the RHEL family the system crypto
# policy (/etc/crypto-policies, pulled in by 50-redhat.conf) sets KexAlgorithms, and
# sshd takes the first value it sees, so the 10- drop-in below is read before it and
# wins. That is the supported route short of writing a crypto-policies subpolicy.
PQ=""
if command -v ssh >/dev/null 2>&1; then
  SUPPORTED=$(ssh -Q kex 2>/dev/null)
  for a in ` + pqKex + `; do
    if echo "$SUPPORTED" | grep -qx "$a"; then PQ="${PQ:+$PQ,}$a"; fi
  done
fi
KEXCUR=$(sudo sshd -T 2>/dev/null | awk '/^kexalgorithms /{print $2; exit}')
KEXLINE=""
KEXNOTE=""
if [ -z "$PQ" ]; then
  KEXNOTE="this OpenSSH offers neither mlkem768x25519-sha256 nor sntrup761x25519-sha512, so key exchange is not post-quantum. Nothing was changed"
elif [ -z "$KEXCUR" ]; then
  KEXNOTE="could not read the current key exchange list from sshd -T. Nothing was changed"
else
  KEXFIRST=${KEXCUR%%,*}
  if echo ",$PQ," | grep -q ",$KEXFIRST,"; then
    # Already first. Keep the line our own drop-in wrote, if any, or the next run
    # would remove it and flap.
    KEXLINE=$(sudo grep -s '^KexAlgorithms ' ` + sshdDropIn + ` | head -1 | cut -d' ' -f2)
  else
    # Hybrid first, then everything the daemon already offers, in its order.
    KEXLINE=$(echo "$KEXCUR" | awk -v pq="$PQ" 'BEGIN{n=split(pq,p,","); for(i=1;i<=n;i++) seen[p[i]]=1}
      {m=split($0,c,","); out=pq; for(i=1;i<=m;i++) if(!(c[i] in seen)) out=out "," c[i]; print out}')
  fi
fi
`

// kexReport emits the key exchange outcome once the drop-in has been dealt with.
// SSHD_STATE is set by the drop-in step: same, would-write or written.
const kexReport = `
# --- post-quantum key exchange: outcome -------------------------------------------
if [ -n "$KEXNOTE" ]; then
  say kex ` + StateReport + ` "$KEXNOTE"
elif [ "$SSHD_STATE" = "written" ] && [ -n "$KEXLINE" ] && ! echo ",$PQ," | grep -q ",${KEXCUR%%,*},"; then
  KEXNOW=$(sudo sshd -T 2>/dev/null | awk '/^kexalgorithms /{print $2; exit}')
  if echo ",$PQ," | grep -q ",${KEXNOW%%,*},"; then
    say kex applied "hybrid key exchange first: ${KEXNOW%%,*} (checked with sshd -T), existing algorithms kept after it"
  else
    say kex ` + StateReport + ` "sshd -T still lists ${KEXNOW%%,*} first after the drop-in; the crypto policy needs a subpolicy. Nothing further was changed"
  fi
elif [ "$SSHD_STATE" = "would-write" ] && [ -n "$KEXLINE" ] && ! echo ",$PQ," | grep -q ",${KEXCUR%%,*},"; then
  say kex would-apply "KexAlgorithms $KEXLINE would be written to the drop-in, existing algorithms kept after the hybrid ones"
else
  say kex already-correct "hybrid key exchange is first: ${KEXCUR%%,*}"
fi
`

// sshFilesAudit fixes permissions under ~/.ssh and moves aside authorized_keys
// backups, then reports the authorized_keys entries without touching them.
const sshFilesAudit = `
# --- ~/.ssh: permissions (fix), backups (fix), stale keys (report) ------------------
SSHDIR="$HOME/.ssh"
loose() { case "$(stat -c %a "$1" 2>/dev/null)" in ?00|"") return 1 ;; *) return 0 ;; esac; }
PERMFIX=0
fixmode() {
  if loose "$1"; then
    CUR=$(stat -c %a "$1")
    PERMFIX=1
    if [ "$DRY" = "1" ]; then say ssh-perms would-apply "$1 is $CUR, would be $2"
    else chmod "$2" "$1" && say ssh-perms applied "$1 was $CUR, now $2"; fi
  fi
}
if [ -d "$SSHDIR" ]; then
  fixmode "$SSHDIR" 700
  for f in "$SSHDIR"/*; do
    [ -f "$f" ] || continue
    case "$(basename "$f")" in
      authorized_keys|config) fixmode "$f" 600 ;;
      authorized_keys.*|known_hosts*|*.pub) ;;
      *) if head -c 512 "$f" 2>/dev/null | grep -q 'PRIVATE KEY'; then fixmode "$f" 600; fi ;;
    esac
  done
  [ "$PERMFIX" = "0" ] && say ssh-perms already-correct "$SSHDIR and the files in it are not group or world accessible"

  # sshd only reads authorized_keys, so a backup beside it does nothing except list
  # who used to have access. Move it out of .ssh, never delete it.
  MOVED=0
  ASIDE="$HOME/.baryovm-moved-keys"
  for f in "$SSHDIR"/authorized_keys.*; do
    [ -f "$f" ] || continue
    MOVED=1
    DEST="$ASIDE/$(basename "$f").$(date +%Y%m%d%H%M%S)"
    if [ "$DRY" = "1" ]; then
      say ssh-backups would-apply "$f would be moved to $DEST"
    else
      mkdir -p "$ASIDE" && chmod 700 "$ASIDE" && mv "$f" "$DEST" && say ssh-backups applied "moved $f to $DEST (nothing deleted)"
    fi
  done
  [ "$MOVED" = "0" ] && say ssh-backups already-correct "no authorized_keys.* backups next to authorized_keys"

  # Report only. Removing a key is a call about people, and it can lock the owner out.
  AK="$SSHDIR/authorized_keys"
  if [ -f "$AK" ]; then
    KEYCOUNT=$(grep -cvE '^[[:space:]]*(#|$)' "$AK")
    COMMENTS=$(grep -vE '^[[:space:]]*(#|$)' "$AK" | awk '{for(i=1;i<=NF;i++) if($i ~ /^(ssh-|ecdsa-|sk-)/){c=$(i+2); print (c==""?"(no comment)":c); next}}' | paste -sd, -)
    say authorized-keys ` + StateReport + ` "$KEYCOUNT key(s): $COMMENTS. Decide which are finished with. No key was removed"
  fi
else
  say ssh-perms already-correct "no $SSHDIR"
fi
`

// systemAudit is the report-only half: updates, unattended upgrades, reboot.
const systemAudit = `
# --- pending updates, unattended upgrades, kernel (all report only) -----------------
# Applying updates can reboot the host, so this only says what is outstanding.
if command -v dnf >/dev/null 2>&1; then
  UPD=$(mktemp)
  sudo dnf -q check-update --security >"$UPD" 2>/dev/null; RC=$?
  if [ "$RC" = "100" ]; then
    N=$(grep -cE '^[A-Za-z0-9_.+-]+\.[A-Za-z0-9_]+ ' "$UPD")
    say updates ` + StateReport + ` "$N security update(s) pending (dnf). Nothing was applied"
  else
    say updates already-correct "no pending security updates (dnf)"
  fi
  rm -f "$UPD"
  if systemctl is-enabled dnf-automatic.timer dnf-automatic-install.timer >/dev/null 2>&1; then
    say unattended-upgrades already-correct "a dnf-automatic timer is enabled"
  else
    say unattended-upgrades ` + StateReport + ` "dnf-automatic is not enabled, so security updates wait for a person"
  fi
elif command -v apt-get >/dev/null 2>&1; then
  N=$(apt-get -s upgrade 2>/dev/null | grep -c '^Inst.*[Ss]ecurity')
  if [ "$N" -gt 0 ]; then
    say updates ` + StateReport + ` "$N security update(s) pending (apt, from the cached package lists). Nothing was applied"
  else
    say updates already-correct "no pending security updates in the cached apt lists"
  fi
  if dpkg -s unattended-upgrades >/dev/null 2>&1 && grep -qs 'Unattended-Upgrade "1"' /etc/apt/apt.conf.d/20auto-upgrades; then
    say unattended-upgrades already-correct "unattended-upgrades is installed and enabled"
  else
    say unattended-upgrades ` + StateReport + ` "unattended-upgrades is not configured, so security updates wait for a person"
  fi
fi

REBOOT=""
if [ -f /var/run/reboot-required ]; then
  REBOOT="/var/run/reboot-required exists"
elif command -v needs-restarting >/dev/null 2>&1; then
  sudo needs-restarting -r >/dev/null 2>&1 || REBOOT="needs-restarting -r says a reboot is required"
fi
if [ -n "$REBOOT" ]; then
  say reboot ` + StateReport + ` "$REBOOT. Not rebooted"
else
  say reboot already-correct "no newer kernel waiting for a reboot"
fi
`

// nginxAudit fixes server_tokens and reports a missing default_server on 443.
// nginx -T is the effective configuration, so conf.d, sites-enabled and includes
// are all covered without guessing at the layout.
const nginxAudit = `
# --- nginx ---------------------------------------------------------------------------
if command -v nginx >/dev/null 2>&1; then
  NGX=$(sudo nginx -T 2>/dev/null)
  TOK=$(echo "$NGX" | grep -E '^[[:space:]]*server_tokens[[:space:]]' | head -1 | sed -E 's/^[[:space:]]*server_tokens[[:space:]]+([a-z]+).*/\1/')
  TOKFILE=/etc/nginx/conf.d/00-baryovm-server-tokens.conf
  if [ "$TOK" = "off" ]; then
    say nginx-tokens already-correct "server_tokens is off"
  elif [ -n "$TOK" ]; then
    # A second server_tokens in the same context is a config error, and editing the
    # owner's own vhost files is not this tool's call.
    say nginx-tokens ` + StateReport + ` "server_tokens is $TOK in the nginx config, so nginx sends its version in the Server header. Set it to off"
  elif [ "$DRY" = "1" ]; then
    say nginx-tokens would-apply "server_tokens off would be written to $TOKFILE"
  else
    printf '# Managed by baryovm vm harden.\nserver_tokens off;\n' | sudo tee "$TOKFILE" >/dev/null
    if sudo nginx -t >/dev/null 2>&1 && sudo nginx -T 2>/dev/null | grep -qE '^[[:space:]]*server_tokens[[:space:]]+off'; then
      sudo systemctl reload nginx >/dev/null 2>&1 || sudo nginx -s reload >/dev/null 2>&1
      say nginx-tokens applied "wrote $TOKFILE and reloaded nginx"
    else
      sudo rm -f "$TOKFILE"
      say nginx-tokens ` + StateReport + ` "nginx rejected server_tokens off through conf.d (or does not include conf.d), so it was removed again"
    fi
  fi

  if echo "$NGX" | grep -qE '^[[:space:]]*listen[[:space:]][^;]*443'; then
    if echo "$NGX" | grep -qE '^[[:space:]]*listen[[:space:]][^;]*443[^;]*default_server'; then
      say nginx-default-server already-correct "a server claims default_server on 443"
    else
      say nginx-default-server ` + StateReport + ` "no server claims default_server on 443, so the first vhost answers for the bare IP. A catch-all changes what an SNI-less client gets, so none was added"
    fi
  fi
fi
`

// firewallSection writes the limit for port 22 into firewalld or ufw, or reports
// when 22 is open to everyone. sshFromGuard has already run when SSHFrom is set.
func firewallSection(o Options) string {
	common := `
# --- port 22 exposure ----------------------------------------------------------------
P22=$(sudo sshd -T 2>/dev/null | awk '/^port /{print $2; exit}')
[ -z "$P22" ] && P22=22
FW=none
if systemctl is-active --quiet firewalld 2>/dev/null; then FW=firewalld
elif command -v ufw >/dev/null 2>&1 && sudo ufw status 2>/dev/null | grep -q '^Status: active'; then FW=ufw
fi
`
	if len(o.SSHFrom) == 0 {
		return common + `
# Report only: closing 22 to the world is a decision, opted into with --ssh-from.
case "$FW" in
  firewalld)
    if sudo firewall-cmd --list-services 2>/dev/null | grep -qw ssh || sudo firewall-cmd --list-ports 2>/dev/null | grep -qw "$P22/tcp"; then
      say ssh-exposure ` + StateReport + ` "port $P22 is open to every source in firewalld. Limit it with --ssh-from <cidr>"
    else
      say ssh-exposure already-correct "port $P22 is not open to every source in firewalld"
    fi ;;
  ufw)
    if sudo ufw status 2>/dev/null | grep -Eq "^($P22|$P22/tcp|OpenSSH)( \(v6\))? +ALLOW +Anywhere"; then
      say ssh-exposure ` + StateReport + ` "port $P22 is open to every source in ufw. Limit it with --ssh-from <cidr>"
    else
      say ssh-exposure already-correct "port $P22 is not open to every source in ufw"
    fi ;;
  *)
    say ssh-exposure ` + StateReport + ` "no host firewall is active, so port $P22 is reachable from anywhere the cloud firewall allows. Enable firewalld or ufw, then limit it with --ssh-from <cidr>"
    ;;
esac
`
	}
	return common + `
case "$FW" in
  firewalld)
    MISSING=0
    for cidr in $SSHFROM; do
      fam=ipv4; case "$cidr" in *:*) fam=ipv6 ;; esac
      RULE="rule family=\"$fam\" source address=\"$cidr\" port port=\"$P22\" protocol=\"tcp\" accept"
      sudo firewall-cmd --permanent --query-rich-rule="$RULE" >/dev/null 2>&1 || MISSING=1
    done
    BROAD=0
    sudo firewall-cmd --permanent --query-service=ssh >/dev/null 2>&1 && BROAD=1
    sudo firewall-cmd --permanent --query-port="$P22/tcp" >/dev/null 2>&1 && BROAD=1
    if [ "$MISSING" = "0" ] && [ "$BROAD" = "0" ]; then
      say ssh-firewall already-correct "firewalld already limits port $P22 to: $SSHFROM"
    elif [ "$DRY" = "1" ]; then
      say ssh-firewall would-apply "firewalld would accept port $P22 only from: $SSHFROM (rich rules added, the open ssh service removed)"
    else
      for cidr in $SSHFROM; do
        fam=ipv4; case "$cidr" in *:*) fam=ipv6 ;; esac
        RULE="rule family=\"$fam\" source address=\"$cidr\" port port=\"$P22\" protocol=\"tcp\" accept"
        sudo firewall-cmd --permanent --add-rich-rule="$RULE" >/dev/null
      done
      for z in $(sudo firewall-cmd --get-active-zones 2>/dev/null | awk '!/^[ \t]/{print $1}'); do
        sudo firewall-cmd --permanent --zone="$z" --remove-service=ssh >/dev/null 2>&1
        sudo firewall-cmd --permanent --zone="$z" --remove-port="$P22/tcp" >/dev/null 2>&1
      done
      sudo firewall-cmd --reload >/dev/null
      LEFT=""
      for z in $(sudo firewall-cmd --get-active-zones 2>/dev/null | awk '!/^[ \t]/{print $1}'); do
        if sudo firewall-cmd --zone="$z" --query-service=ssh >/dev/null 2>&1 || sudo firewall-cmd --zone="$z" --query-port="$P22/tcp" >/dev/null 2>&1; then
          LEFT="$LEFT $z"
        fi
      done
      if [ -z "$LEFT" ]; then
        say ssh-firewall applied "firewalld accepts port $P22 only from: $SSHFROM"
      else
        say ssh-firewall ` + StateReport + ` "firewalld zone(s)$LEFT still accept port $P22 from every source. Remove that service or port by hand"
      fi
    fi ;;
  ufw)
    if [ "$DRY" = "1" ]; then
      say ssh-firewall would-apply "ufw would allow port $P22 only from: $SSHFROM (ufw allow from <cidr> to any port $P22 proto tcp, then the open rule deleted)"
    else
      for cidr in $SSHFROM; do
        sudo ufw allow from "$cidr" to any port "$P22" proto tcp >/dev/null
      done
      sudo ufw delete allow "$P22" >/dev/null 2>&1
      sudo ufw delete allow "$P22/tcp" >/dev/null 2>&1
      sudo ufw delete allow OpenSSH >/dev/null 2>&1
      if sudo ufw status 2>/dev/null | grep -Eq "^($P22|$P22/tcp|OpenSSH)( \(v6\))? +ALLOW +Anywhere"; then
        say ssh-firewall ` + StateReport + ` "ufw still has an allow rule for port $P22 open to Anywhere. Check sudo ufw status numbered and delete it"
      else
        say ssh-firewall applied "ufw allows port $P22 only from: $SSHFROM"
      fi
    fi ;;
  *)
    say ssh-firewall skipped "neither firewalld nor an active ufw was found, so there is no host firewall to write the limit into"
    ;;
esac
`
}
