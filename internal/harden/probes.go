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
	// probeCore is always on: paths no site serves. Each pattern ends at a boundary, so
	// /blog/etc/passwd-guide and /vendor/phpunit-docs are not probes. A boundary may
	// consume the character after the name, which is why the filter suffix allows for it.
	probeCore = []string{
		`/\.(?:env|git|aws|ssh|svn|hg|htpasswd|npmrc|DS_Store)(?:[^A-Za-z0-9]|$)`,
		`/vendor/phpunit/`,
		`/proc/self/environ`,
		`/etc/passwd(?:[^A-Za-z0-9_-]|$)`,
		`jndi:`,
	}
	// probePHP is added only when the owner says no site on the host serves PHP
	// (--nginx-no-php) and nginx -T has no fastcgi_pass. A PHP or WordPress site behind
	// proxy_pass has no fastcgi_pass, so the flag is the owner's word and not a guess. A
	// .php match ends at the end of the path segment, so foo.php.min.js is not a probe.
	probePHP = []string{
		`\.php(?:[/?\s]|$)`,
		`/wp-(?:admin|login|content|includes)(?:[^A-Za-z0-9_-]|$)`,
		`/xmlrpc(?:[^A-Za-z0-9_-]|$)`,
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
	// request line. [^?\s]* cannot cross a ?, so only the path can trigger a ban. The
	// suffix's \s? is there because a pattern's own boundary group may already have taken
	// the space that ends the path; \b would not need it but treats _ as a word character,
	// so /.env_prod would get through.
	// The time is [^\]]* and not +: fail2ban cuts the matched timestamp out of the line
	// before it applies failregex, so the brackets arrive empty.
	// A page on another site can make a visitor's browser request /.env or /?x=/.env, and
	// that must not ban the visitor. The nginx map is on $uri, which has no query either.
	probeFailPrefix = `(?i)^<HOST> \S+ \S+ \[[^\]]*\] "[A-Z]+ [^?\s]*(?:`
	probeFailSuffix = `)\S*\s?HTTP/[0-9.]+"`
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
// prints how many blocks lack it, then one odd:LINE for each server block in a shape it
// does not edit: everything after the brace on the same line, the brace on the next
// line, or CRLF line ends. Blocks inside stream or mail in the same file are left alone,
// since if and return 444 are not valid there.
const includeAwk = `
BEGIN { depth = 0; sdepth = -1; pending = 0; lack = 0; ind = ""; nodd = 0 }
{
  line = $0
  cr = 0
  if (line ~ /\r$/) cr = 1
  if (pending) {
    pending = 0
    live = line
    sub(/\r$/, "", live)
    sub(/[ \t]*#.*$/, "", live)
    if (index(live, inc) == 0) { lack++; if (mode == "edit") print ind inc }
  }
  if (mode == "edit") print line
  c = line
  sub(/\r$/, "", c)
  sub(/[ \t]*#.*$/, "", c)
  if (c ~ /^[ \t]*(stream|mail)[ \t]*[{][ \t]*$/ && sdepth < 0) sdepth = depth
  if (sdepth < 0) {
    if (c ~ /^[ \t]*server[ \t]*[{][ \t]*$/ && !cr) {
      pending = 1
      match(line, /^[ \t]*/)
      ind = substr(line, 1, RLENGTH) "    "
    } else if (c ~ /^[ \t]*server[ \t]*[{]/ || c ~ /^[ \t]*server[ \t]*$/) {
      nodd++
      odd[nodd] = NR
    }
  }
  n = gsub(/[{]/, "{", c)
  m = gsub(/[}]/, "}", c)
  depth += n - m
  if (sdepth >= 0 && depth <= sdepth) sdepth = -1
}
END {
  if (pending) { lack++; if (mode == "edit") print ind inc }
  if (mode == "count") {
    print lack
    for (i = 1; i <= nodd; i++) print "odd:" odd[i]
  }
}
`

// logpathAwk prints every absolute access_log path in nginx -T output, without the quotes
// nginx allows around it. off, syslog:, relative paths and anything with a variable in it
// cannot be watched by file, so they are skipped.
const logpathAwk = `$1 == "access_log" { p = $2; sub(/;$/, "", p); sub(/^["']/, "", p); sub(/["']$/, "", p); if (p ~ /^\// && p !~ /[$]/) print p }`

// nginxProbes is the scanner section: the opt in fail2ban jail and filter, and the opt in
// drop step for nginx itself. A failure in either is collected and reported once, at the
// end, as an ERROR line, so the other step still runs and the run still exits non-zero.
func nginxProbes(o Options) string { return nginxProbesAt(o, "") }

// nginxProbesAt is nginxProbes with every file path under root, which the tests point at a
// temporary directory. Production passes an empty root.
func nginxProbesAt(o Options, root string) string {
	nophp := "0"
	if o.NginxNoPHP {
		nophp = "1"
	}
	return `
# --- nginx scanners: fail2ban filter and jail, optional drop --------------------------
# nginx -T is the effective configuration, so conf.d, sites-enabled and includes are all
# read without guessing at the layout. The jail lives in its own jail.d file: jail.local
# sets backend = systemd for sshd and is not touched here.
PROBE_ERR=""
NOPHP=` + nophp + `
if command -v nginx >/dev/null 2>&1; then
  NGXP=$(sudo -n nginx -T 2>/dev/null)
  PHPTIER=0
  if [ -z "$NGXP" ]; then
    say nginx-probes ` + StateReport + ` "nginx -T printed nothing (the config does not load), so the scanner jail and the drop step were not looked at"
  else
    # The PHP list is off unless the owner passed --nginx-no-php, because a PHP or
    # WordPress site behind proxy_pass has no fastcgi_pass and would look like a host
    # without PHP. Even with the flag, fastcgi_pass keeps it off.
    if [ "$NOPHP" != "1" ]; then
      TIER="core list only (the PHP and WordPress list is off; pass --nginx-no-php if no site on this host serves PHP)"
    # Anywhere on a line that is not a comment: location ~ \.php$ { fastcgi_pass x; }
    # on one line is a common shape, and missing it would ban that site's visitors. nginx
    # also allows the argument on the next line, so the name may end the line.
    elif echo "$NGXP" | grep -vE '^[[:space:]]*#' | grep -qE '(^|[;{[:space:]])fastcgi_pass([[:space:]]|$)'; then
      TIER="core list only (nginx has fastcgi_pass, so PHP and WordPress paths are left alone)"
      say nginx-php ` + StateReport + ` "--nginx-no-php was given but nginx runs PHP (fastcgi_pass found), so .php, wp-admin, wp-login, wp-content, wp-includes and xmlrpc are not banned or dropped"
    else
      PHPTIER=1
      TIER="core list plus PHP and WordPress paths (--nginx-no-php, and no fastcgi_pass in nginx)"
    fi
` + probeJailSection(o, root) + probeDropSection(o, root) + `
  fi
fi
if [ -n "$PROBE_ERR" ]; then
  echo "ERROR|$PROBE_ERR"
  exit 1
fi
`
}

// probeJailSection is opt in (--nginx-ban-probes). Two tradeoffs are why: a page on another
// site can make a visitor's browser request a scanner path, and behind a proxy that nginx
// does not see through the jail would ban the proxy. Without the flag it only reports, and
// it never removes a jail file an earlier run wrote. With it, it writes the filter and the
// jail. It restores what was there, or removes what it created, when fail2ban -t rejects
// the result, and reloads only on success. A rejected jail and a failed reload are errors.
func probeJailSection(o Options, root string) string {
	head := `
    if ! command -v fail2ban-client >/dev/null 2>&1; then
      say nginx-probes skipped "fail2ban is not installed, so there is nothing to ban scanners with"
    else
      PFILTER=` + sshx.Quote(root+probeFilterFile) + `
      PJAIL=` + sshx.Quote(root+probeJailFile) + `
`
	if !o.NginxBanProbes {
		return head + `
      if sudo -n test -f "$PJAIL" || sudo -n test -f "$PFILTER"; then
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

      if sudo -n test -f "$PFILTER" && sudo -n cmp -s "$NEWFILT" "$PFILTER" && sudo -n test -f "$PJAIL" && sudo -n cmp -s "$NEWJAIL" "$PJAIL"; then
        say nginx-probes already-correct "scanner jail and filter match policy: $TIER"
      elif [ "$DRY" = "1" ]; then
        say nginx-probes would-apply "$PFILTER and $PJAIL would be written: $TIER"
      else
        PREVFILT=""
        PREVJAIL2=""
        if sudo -n test -f "$PFILTER"; then PREVFILT=$(mktemp); sudo -n cat "$PFILTER" > "$PREVFILT"; fi
        if sudo -n test -f "$PJAIL"; then PREVJAIL2=$(mktemp); sudo -n cat "$PJAIL" > "$PREVJAIL2"; fi
        sudo -n mkdir -p "$(dirname "$PFILTER")" "$(dirname "$PJAIL")"
        sudo -n install -m 644 -o root -g root "$NEWFILT" "$PFILTER"
        sudo -n install -m 644 -o root -g root "$NEWJAIL" "$PJAIL"
        if sudo -n fail2ban-client -t >/dev/null 2>&1; then
          sudo -n systemctl enable --now fail2ban >/dev/null 2>&1
          if sudo -n systemctl reload fail2ban >/dev/null 2>&1 || sudo -n systemctl restart fail2ban >/dev/null 2>&1; then
            say nginx-probes applied "wrote $PFILTER and $PJAIL and reloaded fail2ban: $TIER"
          else
            PROBE_ERR="${PROBE_ERR:+$PROBE_ERR; }the scanner jail files were written and fail2ban accepted them, but fail2ban could not be reloaded or restarted, so the jail is not active"
          fi
        else
          # Same reasoning as jail.local: a rejected file must not cost the host the one
          # it already had.
          if [ -n "$PREVFILT" ]; then sudo -n install -m 644 -o root -g root "$PREVFILT" "$PFILTER"; else sudo -n rm -f "$PFILTER"; fi
          if [ -n "$PREVJAIL2" ]; then sudo -n install -m 644 -o root -g root "$PREVJAIL2" "$PJAIL"; else sudo -n rm -f "$PJAIL"; fi
          PROBE_ERR="${PROBE_ERR:+$PROBE_ERR; }fail2ban rejected the scanner jail, so the previous files were restored (or the new ones removed) and fail2ban was not reloaded"
        fi
        [ -n "$PREVFILT" ] && rm -f "$PREVFILT"
        [ -n "$PREVJAIL2" ] && rm -f "$PREVJAIL2"
      fi
      rm -f "$NEWFILT" "$NEWJAIL"
    fi
`
}

// probeDropSection counts the server blocks without the include, and with
// --nginx-drop-probes writes the map and the snippet and adds the include. Server blocks
// in a shape the awk does not edit are counted apart and never read as already-correct.
func probeDropSection(o Options, root string) string {
	common := `
    # --- drop: server blocks that lack the include --------------------------------------
    PMAP=` + sshx.Quote(root+probeMapFile) + `
    PSNIP=` + sshx.Quote(root+probeSnippet) + `
    PINC="include $PSNIP;"
    INCAWK=` + sshx.Quote(includeAwk) + `
    NGXFILES=$(echo "$NGXP" | awk '/^# configuration file /{f=$4; sub(/:$/, "", f); print f}' | sort -u)
    LACK=0
    LACKFILES=""
    ODD=0
    ODDLIST=""
    for f in $NGXFILES; do
      case "$f" in "$PMAP"|"$PSNIP") continue ;; esac
      OUT=$(sudo -n cat "$f" 2>/dev/null | awk -v mode=count -v inc="$PINC" "$INCAWK")
      N=$(echo "$OUT" | head -1)
      case "$N" in ''|*[!0-9]*) N=0 ;; esac
      if [ "$N" -gt 0 ]; then LACK=$((LACK + N)); LACKFILES="$LACKFILES $f"; fi
      for ln in $(echo "$OUT" | sed -n 's/^odd://p'); do
        ODD=$((ODD + 1))
        if [ "$ODD" -le 5 ]; then ODDLIST="${ODDLIST:+$ODDLIST, }$f:$ln"; fi
      done
    done
    [ "$ODD" -gt 5 ] && ODDLIST="$ODDLIST, ..."
`
	oddReport := `
    if [ "$ODD" -gt 0 ]; then
      say nginx-drop ` + StateReport + ` "$ODD server block(s) in a shape this step does not edit (one line, the brace on the next line, or CRLF line ends): $ODDLIST. Add $PINC by hand"
    fi
`
	if !o.NginxDropProbes {
		return common + `
    if [ "$LACK" = "0" ] && [ "$ODD" = "0" ]; then
      say nginx-drop already-correct "every server block has $PINC"
    elif [ "$LACK" -gt 0 ]; then
      say nginx-drop ` + StateReport + ` "$LACK server block(s) lack $PINC, so scanner requests still reach the apps. Add it with --nginx-drop-probes"
    fi
` + oddReport
	}
	return common + `
    NEWMAP=$(mktemp)
    NEWSNIP=$(mktemp)
    {
      echo "# Managed by baryovm vm harden. Edits here are overwritten on the next run."
      echo "# $TIER"
      echo "# Case insensitive (~*), like the fail2ban filter ((?i)), so both agree on what a probe is."
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

    if [ "$LACK" = "0" ] && sudo -n test -f "$PMAP" && sudo -n cmp -s "$NEWMAP" "$PMAP" && sudo -n test -f "$PSNIP" && sudo -n cmp -s "$NEWSNIP" "$PSNIP"; then
      if [ "$ODD" = "0" ]; then
        say nginx-drop already-correct "map, snippet and the include in every server block match policy: $TIER"
      fi
    elif [ "$DRY" = "1" ]; then
      say nginx-drop would-apply "$PMAP and $PSNIP would be written and $PINC added to $LACK server block(s): $TIER"
    else
      BK=$(mktemp -d)
      if sudo -n test -f "$PMAP"; then sudo -n cat "$PMAP" > "$BK/map"; fi
      if sudo -n test -f "$PSNIP"; then sudo -n cat "$PSNIP" > "$BK/snip"; fi
      for f in $LACKFILES; do sudo -n cat "$f" > "$BK/file.$(echo "$f" | tr '/' '_')"; done
      WROTE=1
      sudo -n mkdir -p "$(dirname "$PMAP")" "$(dirname "$PSNIP")" || WROTE=0
      sudo -n install -m 644 -o root -g root "$NEWMAP" "$PMAP" || WROTE=0
      sudo -n install -m 644 -o root -g root "$NEWSNIP" "$PSNIP" || WROTE=0
      for f in $LACKFILES; do
        EDITED=$(mktemp)
        sudo -n cat "$f" | awk -v mode=edit -v inc="$PINC" "$INCAWK" > "$EDITED"
        sudo -n tee "$f" < "$EDITED" >/dev/null
        rm -f "$EDITED"
      done
      NGXTOUT="could not write the map or the snippet"
      if [ "$WROTE" = "1" ] && NGXTOUT=$(sudo -n nginx -t 2>&1); then
        if sudo -n systemctl reload nginx >/dev/null 2>&1 || sudo -n nginx -s reload >/dev/null 2>&1; then
          say nginx-drop applied "wrote $PMAP and $PSNIP, added $PINC to $LACK server block(s), reloaded nginx: $TIER"
        else
          PROBE_ERR="${PROBE_ERR:+$PROBE_ERR; }the drop step was written and nginx -t passed, but nginx could not be reloaded, so the running nginx does not drop probes yet"
        fi
      else
        # Every file goes back: the ones that existed get their old text, the ones this
        # step created are removed. nginx is not reloaded, so it never saw the change.
        # Say where nginx complained, since a stream or mail block in an included file
        # is the usual cause and the awk cannot see it.
        WHERE=$(echo "$NGXTOUT" | sed -n 's/.* in \(\/[^ ]*:[0-9][0-9]*\).*/\1/p' | head -1 | tr -cd '[:print:]')
        for f in $LACKFILES; do sudo -n tee "$f" < "$BK/file.$(echo "$f" | tr '/' '_')" >/dev/null; done
        if [ -f "$BK/map" ]; then sudo -n tee "$PMAP" < "$BK/map" >/dev/null; else sudo -n rm -f "$PMAP"; fi
        if [ -f "$BK/snip" ]; then sudo -n tee "$PSNIP" < "$BK/snip" >/dev/null; else sudo -n rm -f "$PSNIP"; fi
        REASON="nginx -t rejected the drop step at ${WHERE:-an unknown file and line}"
        [ "$WROTE" = "1" ] || REASON="the map or the snippet could not be written"
        PROBE_ERR="${PROBE_ERR:+$PROBE_ERR; }$REASON, so every file was restored and nginx was not reloaded. The map must load before the server blocks that use it, and a server block included from inside stream or mail cannot take the include"
      fi
      rm -rf "$BK"
    fi
    rm -f "$NEWMAP" "$NEWSNIP"
` + oddReport
}
