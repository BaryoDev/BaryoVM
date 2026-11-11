// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package harden

import (
	"strings"

	"github.com/BaryoDev/BaryoVM/internal/sshx"
)

// Scanner paths. These strings are the one source for both consumers: the fail2ban
// filter reads them as Python regex against a log line, and the nginx map reads them as
// PCRE against $uri. They stay inside the syntax RE2 shares with both, so a test can run
// them with Go's regexp. No pattern may contain a single quote, a double quote, a
// semicolon, a brace or a space, because they are written inside shell and nginx quoting.
//
// The boundary after a dotfile name keeps .github and .environment out while still
// catching .env.local and .git/config. Nothing here matches /.well-known/, which
// certificate renewal needs.
var (
	// probeCore is always on: paths no site serves.
	probeCore = []string{
		`/\.(?:env|git|aws|ssh|svn|hg|htpasswd|npmrc|DS_Store)\b`,
		`vendor/phpunit`,
		`/proc/self/environ`,
		`etc/passwd`,
		`jndi:`,
	}
	// probePHP is added only when nginx -T has no fastcgi_pass. On a host that runs PHP
	// these are real pages, and banning their visitors would be a self-inflicted outage.
	probePHP = []string{
		`\.php\b`,
		`/wp-(?:admin|login|content|includes)`,
		`/xmlrpc`,
	}
)

const (
	probeFilterFile = "/etc/fail2ban/filter.d/baryovm-nginx-probes.conf"
	probeJailFile   = "/etc/fail2ban/jail.d/baryovm-nginx-probes.local"
	probeMapFile    = "/etc/nginx/conf.d/00-baryovm-probes.conf"
	probeSnippet    = "/etc/nginx/baryovm/drop-probes.conf"
	probeDefaultLog = "/var/log/nginx/access.log"

	// probeIgnoreIP keeps jail.local's list through known/ and adds the private ranges, so
	// a proxy or load balancer on a private address is never banned.
	probeIgnoreIP = "ignoreip = %(known/ignoreip)s 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 fc00::/7"

	// probeFailPrefix and probeFailSuffix wrap the alternation of paths. The shape is the
	// start of the nginx combined and main formats: address, identd, user, time, then the
	// request line. [^?\s]* cannot cross a ?, so only the path can trigger a ban.
	// A page on another site can make a visitor's browser request /.env or /?x=/.env, and
	// that must not ban the visitor. The nginx map is on $uri, which has no query either.
	probeFailPrefix = `^<HOST> \S+ \S+ \[[^\]]+\] "[A-Z]+ [^?\s]*(?:`
	probeFailSuffix = `)\S* HTTP/[0-9.]+"`
)

// failregexLine is the whole failregex line, with the PHP tier when php is true.
func failregexLine(php bool) string {
	alts := append([]string{}, probeCore...)
	if php {
		alts = append(alts, probePHP...)
	}
	return probeFailPrefix + strings.Join(alts, "|") + probeFailSuffix
}

// shellList renders patterns as space separated single-quoted shell words.
func shellList(ps []string) string {
	q := make([]string, len(ps))
	for i, p := range ps {
		q[i] = sshx.Quote(p)
	}
	return strings.Join(q, " ")
}

// includeAwk rewrites one nginx file. In edit mode it prints the file with inc added
// right after every server block opener that does not already have it. In count mode it
// prints how many blocks lack it. Blocks inside stream are left alone, since if and
// return 444 are not valid there. Only an opener alone on its line is touched.
const includeAwk = `
BEGIN { depth = 0; sdepth = -1; pending = 0; lack = 0; ind = "" }
{
  line = $0
  if (pending) {
    pending = 0
    if (index(line, inc) == 0) { lack++; if (mode == "edit") print ind inc }
  }
  if (mode == "edit") print line
  c = line
  sub(/[ \t]*#.*$/, "", c)
  if (c ~ /^[ \t]*stream[ \t]*[{][ \t]*$/ && sdepth < 0) sdepth = depth
  if (c ~ /^[ \t]*server[ \t]*[{][ \t]*$/ && sdepth < 0) {
    pending = 1
    match(line, /^[ \t]*/)
    ind = substr(line, 1, RLENGTH) "    "
  }
  n = gsub(/[{]/, "{", c)
  m = gsub(/[}]/, "}", c)
  depth += n - m
  if (sdepth >= 0 && depth <= sdepth) sdepth = -1
}
END {
  if (pending) { lack++; if (mode == "edit") print ind inc }
  if (mode == "count") print lack
}
`

// logpathAwk prints every absolute access_log path in nginx -T output. off, syslog:,
// relative paths and anything with a variable in it cannot be watched by file, so they
// are skipped.
const logpathAwk = `$1 == "access_log" { p = $2; sub(/;$/, "", p); if (p ~ /^\// && p !~ /[$]/) print p }`

// nginxProbes is the scanner section: a fail2ban jail and filter whenever nginx and
// fail2ban are both present, and the opt in drop step for nginx itself.
func nginxProbes(o Options) string {
	return `
# --- nginx scanners: fail2ban filter and jail, optional drop --------------------------
# nginx -T is the effective configuration, so conf.d, sites-enabled and includes are all
# read without guessing at the layout. The jail lives in its own jail.d file: jail.local
# sets backend = systemd for sshd and is not touched here.
if command -v nginx >/dev/null 2>&1; then
  NGXP=$(sudo nginx -T 2>/dev/null)
  PHPTIER=0
  if [ -z "$NGXP" ]; then
    say nginx-probes ` + StateReport + ` "nginx -T printed nothing (the config does not load), so the scanner jail and the drop step were not looked at"
  else
    # A host that runs PHP serves .php, wp-* and xmlrpc paths for real. Only a host with
    # no fastcgi_pass gets those added, and one that has it keeps the narrow list.
    if echo "$NGXP" | grep -qE '^[[:space:]]*fastcgi_pass[[:space:]]'; then
      TIER="core list only (nginx has fastcgi_pass, so PHP and WordPress paths are left alone)"
      say nginx-php ` + StateReport + ` "nginx runs PHP (fastcgi_pass found), so .php, wp-admin, wp-login, wp-content, wp-includes and xmlrpc are not banned or dropped"
    else
      PHPTIER=1
      TIER="core list plus PHP and WordPress paths (no fastcgi_pass in nginx)"
    fi
` + probeJailSection(o) + probeDropSection(o) + `
  fi
fi
`
}

// probeJailSection is opt in (--nginx-ban-probes). Two tradeoffs are why: a page on another
// site can make a visitor's browser request a scanner path, and behind a proxy that nginx
// does not see through the jail would ban the proxy. Without the flag it only reports, and
// it never removes a jail file an earlier run wrote. With it, it writes the filter and the
// jail. It restores what was there, or removes
// what it created, when fail2ban -t rejects the result, and reloads only on success.
func probeJailSection(o Options) string {
	head := `
    if ! command -v fail2ban-client >/dev/null 2>&1; then
      say nginx-probes skipped "fail2ban is not installed, so there is nothing to ban scanners with"
    else
      PFILTER=` + sshx.Quote(probeFilterFile) + `
      PJAIL=` + sshx.Quote(probeJailFile) + `
`
	if !o.NginxBanProbes {
		return head + `
      if sudo test -f "$PJAIL" || sudo test -f "$PFILTER"; then
        say nginx-probes ` + StateReport + ` "an earlier run wrote $PJAIL. It is left as it is and not updated. The scanner jail bans sources that request paths like .env and .git (path only, never the query string); run with --nginx-ban-probes to update it"
      else
        say nginx-probes ` + StateReport + ` "a fail2ban jail for nginx scanners is available: it bans sources that request .env, .git, .aws and similar paths. Two tradeoffs: another site can make a visitor's browser request such a path, and behind a CDN or load balancer the ban lands on the proxy. Enable it with --nginx-ban-probes"
      fi
    fi
`
	}
	return head + `
      LOGS=$(echo "$NGXP" | awk ` + sshx.Quote(logpathAwk) + ` | sort -u)
      [ -z "$LOGS" ] && LOGS=` + sshx.Quote(probeDefaultLog) + `
      # nginx logs the connecting address. Without set_real_ip_from that is the proxy's,
      # and the jail would ban the proxy and cut off everyone behind it.
      if ! echo "$NGXP" | grep -qE '^[[:space:]]*set_real_ip_from[[:space:]]'; then
        say nginx-realip ` + StateReport + ` "nginx has no set_real_ip_from, so it logs the connecting address. Behind a CDN or load balancer the scanner jail would ban the proxy and cut off everyone. Configure set_real_ip_from first"
      fi
      PHPALT=""
      if [ "$PHPTIER" = "1" ]; then
        PHPALT=` + sshx.Quote("|"+strings.Join(probePHP, "|")) + `
      fi
      NEWFILT=$(mktemp)
      NEWJAIL=$(mktemp)
      {
        echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
        echo "[Definition]"
        printf '%s%s%s\n' ` + sshx.Quote("failregex = "+probeFailPrefix+strings.Join(probeCore, "|")) + ` "$PHPALT" ` + sshx.Quote(probeFailSuffix) + `
        echo "ignoreregex ="
      } > "$NEWFILT"
      {
        echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
        echo "# backend = auto because jail.local sets systemd for sshd, and this jail reads files."
        echo "[baryovm-nginx-probes]"
        echo "enabled = true"
        echo "filter = baryovm-nginx-probes"
        echo "backend = auto"
        echo "port = http,https"
        echo '` + probeIgnoreIP + `'
        echo "maxretry = 5"
        echo "findtime = 10m"
        FIRSTLOG=1
        for lp in $LOGS; do
          if [ "$FIRSTLOG" = "1" ]; then echo "logpath = $lp"; FIRSTLOG=0; else echo "          $lp"; fi
        done
      } > "$NEWJAIL"

      if sudo test -f "$PFILTER" && sudo cmp -s "$NEWFILT" "$PFILTER" && sudo test -f "$PJAIL" && sudo cmp -s "$NEWJAIL" "$PJAIL"; then
        say nginx-probes already-correct "scanner jail and filter match policy: $TIER"
      elif [ "$DRY" = "1" ]; then
        say nginx-probes would-apply "$PFILTER and $PJAIL would be written: $TIER"
      else
        PREVFILT=""
        PREVJAIL2=""
        if sudo test -f "$PFILTER"; then PREVFILT=$(mktemp); sudo cat "$PFILTER" > "$PREVFILT"; fi
        if sudo test -f "$PJAIL"; then PREVJAIL2=$(mktemp); sudo cat "$PJAIL" > "$PREVJAIL2"; fi
        sudo mkdir -p /etc/fail2ban/filter.d /etc/fail2ban/jail.d
        sudo install -m 644 -o root -g root "$NEWFILT" "$PFILTER"
        sudo install -m 644 -o root -g root "$NEWJAIL" "$PJAIL"
        if sudo fail2ban-client -t >/dev/null 2>&1; then
          sudo systemctl enable --now fail2ban >/dev/null 2>&1
          sudo systemctl reload fail2ban >/dev/null 2>&1 || sudo systemctl restart fail2ban >/dev/null 2>&1
          say nginx-probes applied "wrote $PFILTER and $PJAIL and reloaded fail2ban: $TIER"
        else
          # Same reasoning as jail.local: a rejected file must not cost the host the one
          # it already had.
          if [ -n "$PREVFILT" ]; then sudo install -m 644 -o root -g root "$PREVFILT" "$PFILTER"; else sudo rm -f "$PFILTER"; fi
          if [ -n "$PREVJAIL2" ]; then sudo install -m 644 -o root -g root "$PREVJAIL2" "$PJAIL"; else sudo rm -f "$PJAIL"; fi
          say nginx-probes ` + StateReport + ` "fail2ban rejected the scanner jail, so the previous files were restored (or the new ones removed) and fail2ban was not reloaded"
        fi
        [ -n "$PREVFILT" ] && rm -f "$PREVFILT"
        [ -n "$PREVJAIL2" ] && rm -f "$PREVJAIL2"
      fi
      rm -f "$NEWFILT" "$NEWJAIL"
    fi
`
}

// probeDropSection counts the server blocks without the include, and with
// --nginx-drop-probes writes the map and the snippet and adds the include.
func probeDropSection(o Options) string {
	common := `
    # --- drop: server blocks that lack the include --------------------------------------
    PMAP=` + sshx.Quote(probeMapFile) + `
    PSNIP=` + sshx.Quote(probeSnippet) + `
    PINC="include $PSNIP;"
    INCAWK=` + sshx.Quote(includeAwk) + `
    NGXFILES=$(echo "$NGXP" | awk '/^# configuration file /{f=$4; sub(/:$/, "", f); print f}' | sort -u)
    LACK=0
    LACKFILES=""
    for f in $NGXFILES; do
      case "$f" in "$PMAP"|"$PSNIP") continue ;; esac
      N=$(sudo cat "$f" 2>/dev/null | awk -v mode=count -v inc="$PINC" "$INCAWK")
      case "$N" in ''|*[!0-9]*) N=0 ;; esac
      if [ "$N" -gt 0 ]; then LACK=$((LACK + N)); LACKFILES="$LACKFILES $f"; fi
    done
`
	if !o.NginxDropProbes {
		return common + `
    if [ "$LACK" = "0" ]; then
      say nginx-drop already-correct "every server block has $PINC"
    else
      say nginx-drop ` + StateReport + ` "$LACK server block(s) lack $PINC, so scanner requests still reach the apps. Add it with --nginx-drop-probes"
    fi
`
	}
	return common + `
    NEWMAP=$(mktemp)
    NEWSNIP=$(mktemp)
    {
      echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
      echo "# $TIER"
      echo 'map $uri $baryovm_probe {'
      echo '    default 0;'
      for p in ` + shellList(probeCore) + `; do printf '    "~*%s" 1;\n' "$p"; done
      if [ "$PHPTIER" = "1" ]; then
        for p in ` + shellList(probePHP) + `; do printf '    "~*%s" 1;\n' "$p"; done
      fi
      echo '}'
    } > "$NEWMAP"
    {
      echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
      echo 'if ($baryovm_probe) {'
      echo '    return 444;'
      echo '}'
    } > "$NEWSNIP"

    if [ "$LACK" = "0" ] && sudo test -f "$PMAP" && sudo cmp -s "$NEWMAP" "$PMAP" && sudo test -f "$PSNIP" && sudo cmp -s "$NEWSNIP" "$PSNIP"; then
      say nginx-drop already-correct "map, snippet and the include in every server block match policy: $TIER"
    elif [ "$DRY" = "1" ]; then
      say nginx-drop would-apply "$PMAP and $PSNIP would be written and $PINC added to $LACK server block(s): $TIER"
    else
      BK=$(mktemp -d)
      if sudo test -f "$PMAP"; then sudo cat "$PMAP" > "$BK/map"; fi
      if sudo test -f "$PSNIP"; then sudo cat "$PSNIP" > "$BK/snip"; fi
      for f in $LACKFILES; do sudo cat "$f" > "$BK/file.$(echo "$f" | tr '/' '_')"; done
      sudo mkdir -p "$(dirname "$PSNIP")"
      sudo install -m 644 -o root -g root "$NEWMAP" "$PMAP"
      sudo install -m 644 -o root -g root "$NEWSNIP" "$PSNIP"
      for f in $LACKFILES; do
        EDITED=$(mktemp)
        sudo cat "$f" | awk -v mode=edit -v inc="$PINC" "$INCAWK" > "$EDITED"
        sudo tee "$f" < "$EDITED" >/dev/null
        rm -f "$EDITED"
      done
      if sudo nginx -t >/dev/null 2>&1; then
        sudo systemctl reload nginx >/dev/null 2>&1 || sudo nginx -s reload >/dev/null 2>&1
        say nginx-drop applied "wrote $PMAP and $PSNIP, added $PINC to $LACK server block(s), reloaded nginx: $TIER"
      else
        # Every file goes back: the ones that existed get their old text, the ones this
        # step created are removed. nginx is not reloaded, so it never saw the change.
        for f in $LACKFILES; do sudo tee "$f" < "$BK/file.$(echo "$f" | tr '/' '_')" >/dev/null; done
        if [ -f "$BK/map" ]; then sudo tee "$PMAP" < "$BK/map" >/dev/null; else sudo rm -f "$PMAP"; fi
        if [ -f "$BK/snip" ]; then sudo tee "$PSNIP" < "$BK/snip" >/dev/null; else sudo rm -f "$PSNIP"; fi
        say nginx-drop ` + StateReport + ` "nginx -t rejected the drop step, so every file was restored and nginx was not reloaded. The map must load before the server blocks that use it"
      fi
      rm -rf "$BK"
    fi
    rm -f "$NEWMAP" "$NEWSNIP"
`
}
