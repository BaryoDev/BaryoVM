# Real host pass: Azure Ubuntu 24.04, x86_64

Hand run for issue #110, mirroring the pass from #79. Date: 2026-10-20.

The host is a throwaway Azure VM ("the VM" below): Ubuntu 24.04.5 LTS, kernel 7.0.0-1017-azure, size Standard_D2als_v6, login user `azureuser`. Azure's network security group allowed 22, 80 and 443 only from the machine running the pass. The VM's own firewall (ufw) was not inspected during the pass, and the VM was deleted after it, so its state is not recorded. Port 80 answered from outside the VM (section 4), so nothing on the host blocked it. Port 443 was not exercised. It is registered as `azure-x86`.

BaryoVM state was kept apart from the real fleet with `BARYOVM_HOME` pointing at an empty directory, so no other VM entry was read or written. The throwaway content (a one-line `index.html`, `traefik/whoami`, `postgres:16-alpine`) lived under `/tmp` on the local machine and was never from a real project. Long output is trimmed. Errors are whole.

## Result

| Step | Outcome |
|---|---|
| `vm add`, `vm ping` | pass |
| `vm bootstrap` (twice) | pass, idempotent |
| `vm harden` (dry run, apply, apply again) | pass on the `apt-get` path, SSH still works after |
| `vm health` | not a command (issue #41); `vm threats` and `vm ping` used instead. Unknown subcommand exits 0 |
| static site `stack release` x2 | pass, site served from outside the VM |
| container stack `stack release` x2 | first release fails on the pre-release backup, passes with `--no-backup`; second pass |
| verify | no `stack verify` command; the release verify plan works and fails a release correctly |
| `stack backup`, `stack restore` | pass, data came back |
| tear down, `vm remove` | pass |

## 1. Register and inspect

```
$ baryovm vm add azure-x86 --host <the VM> --user azureuser --key <key>
✓ registered azure-x86 (azureuser@<the VM>)

$ baryovm vm ping azure-x86
  host: Linux 7.0.0-1017-azure
  docker: not installed
✓ azure-x86 reachable
! learned the host key for <the VM>:22: SHA256:...
  verify it: ssh-keyscan -t ecdsa,ed25519 <the VM>:22 | ssh-keygen -lf -

$ baryovm vm exec azure-x86 -- cat /etc/os-release
PRETTY_NAME="Ubuntu 24.04.5 LTS"
...
```

## 2. Bootstrap

```
$ baryovm vm bootstrap azure-x86
✓ azure-x86 ready: Docker version 29.9.1, build dfc520d

$ baryovm vm bootstrap azure-x86
✓ azure-x86 already had Docker: Docker version 29.9.1, build dfc520d
```

After bootstrap the VM had Docker Compose v5.6.0 and rsync 3.2.7, and the `azureuser` account was in the `docker` and `sudo` groups with passwordless sudo.

## 3. Harden

The VM's sshd is OpenSSH 9.6p1, so `PerSourcePenalties` (9.8 and up) is not available. harden says so and relies on fail2ban.

```
$ baryovm vm harden azure-x86 --dry-run
▸ Hardening azure-x86 (ubuntu)
  sshd: OpenSSH_9.6p1 Ubuntu-3ubuntu13.20, OpenSSL 3.0.13 30 Jan 2024
! this sshd has no PerSourcePenalties (needs OpenSSH 9.8+); fail2ban is doing all the work
  sshd: would-apply: /etc/ssh/sshd_config.d/10-baryovm-hardening.conf would be written
  fail2ban: would-apply: would install fail2ban
  fail2ban: inactive
! dry run: nothing was changed

$ baryovm vm harden azure-x86
  sshd: applied: wrote /etc/ssh/sshd_config.d/10-baryovm-hardening.conf and reloaded
  fail2ban: applied: installed
  jail: applied: wrote jail.local and started fail2ban
  fail2ban: active
✓ policy applied to azure-x86

$ baryovm vm ping azure-x86
  host: Linux 7.0.0-1017-azure
  docker: Docker version 29.9.1, build dfc520d
✓ azure-x86 reachable

$ baryovm vm harden azure-x86          (second run)
  sshd: already-correct: /etc/ssh/sshd_config.d/10-baryovm-hardening.conf matches policy
  fail2ban: already-correct: already installed
  jail: already-correct: /etc/fail2ban/jail.local matches policy
  fail2ban: active
```

The ping after harden opens a new SSH connection, so key login still works with the policy in place. `systemctl is-active` reported `active` for both `fail2ban` and `ssh`.

### Health

`vm health` does not exist yet (issue #41). It prints the `vm` help and exits 0, which is its own problem (issue #120). `vm threats` was used as the closest check:

```
$ baryovm vm threats azure-x86
▸ azure-x86, since 24 hours ago
  failed attempts: 0 from 1 sources
  invalid users: 0
  fail2ban: active, 0 banned to date, 0 banned now
▸ Accepted logins
  azureuser@<this machine>: publickey, 13 time(s)
```

## 4. Static site stack

The Oracle VM's static recipe expects nginx on the host. Bootstrap does not install it (issue #18 covers a site command), so nginx was installed by hand through `vm exec`, with a server block serving `/var/www/bvm110` on port 80.

Manifest:

```json
{
  "localRoot": "<local dir>/site",
  "remoteRoot": "/var/www/bvm110",
  "sync": ["out/"],
  "builds": [],
  "sudo": true,
  "noCompose": true,
  "postDeploy": ["nginx -t", "systemctl reload nginx"],
  "verify": ["curl -fsS http://127.0.0.1/ | grep -q bvm110"]
}
```

```
$ baryovm stack add bvm110-site --vm azure-x86 --path /var/www/bvm110 --release-file <manifest>
✓ registered stack bvm110-site (/var/www/bvm110 on azure-x86)

$ baryovm stack release bvm110-site
✓ bvm110-site released
✓ bvm110-site: release done

$ baryovm stack release bvm110-site        (the #57 case, second in a row)
✓ bvm110-site released
✓ bvm110-site: release done
```

The destination directory did not exist and rsync created it. From the local machine, over the internet and through the NSG:

```
$ curl -sS -i http://<the VM>/
HTTP/1.1 200 OK
Server: nginx/1.24.0 (Ubuntu)
Content-Type: text/html
...
<!doctype html><title>bvm110</title><h1>bvm110 static v1</h1>
```

A third release after changing the page served `static v2` from outside. Files arrive owned by the local uid (501), as the sudo section of USAGE.md says (`-a` with a root receiver keeps ownership). nginx can read them, so it makes no difference here.

## 5. Container stack

Compose file: `traefik/whoami` bound to `127.0.0.1:8081`, plus `postgres:16-alpine` as the database, both with fixed `container_name`s. The registration has a database, so backup and restore apply.

```
$ baryovm stack add bvm110-ctr --vm azure-x86 --path /home/azureuser/bvm110-ctr --release-file <manifest> \
    --db-container bvm110-db --db-name bvm110 --backup-dir /home/azureuser/bvm110-backups --keep 3
✓ registered stack bvm110-ctr (/home/azureuser/bvm110-ctr on azure-x86)

$ baryovm stack release bvm110-ctr
! could not check container names before compose up: remote `cd '/home/azureuser/bvm110-ctr' && docker compose config --format json`: Process exited with status 1: bash: line 1: cd: /home/azureuser/bvm110-ctr: No such file or directory
✗ stack release: pre-release backup failed: remote `set -e
BK='/home/azureuser/bvm110-backups'
mkdir -p "$BK"
...
docker exec 'bvm110-db' pg_dump -U 'postgres' -Fc 'bvm110' > "$BK/db-$ts.dump"
...`: Process exited with status 1: Error response from daemon: No such container: bvm110-db
✗ baryovm: pre-release backup failed: (same text again)
[exit 1]
```

Nothing exists on a first release, so the backup cannot work (issue #121). Workaround, once:

```
$ baryovm stack release bvm110-ctr --no-backup
! could not check container names before compose up: ... No such file or directory
✓ bvm110-ctr released
✓ bvm110-ctr: release done

$ baryovm stack ps bvm110-ctr
NAME            IMAGE                   SERVICE   STATUS         PORTS
bvm110-db       postgres:16-alpine      db        Up 7 seconds   5432/tcp
bvm110-whoami   traefik/whoami:latest   whoami    Up 7 seconds   127.0.0.1:8081->80/tcp

$ baryovm stack release bvm110-ctr         (second in a row, with the backup)
✓ bvm110-ctr released
✓ bvm110-ctr: release done

$ baryovm -o json stack release bvm110-ctr (third)
{ "ok": true, "action": "stack release", "message": "bvm110-ctr released" }
```

No 502 or name collision on the repeated releases (the #57 case). `stack backups` then listed two dumps, one per release that ran a backup.

## 6. Verify

There is no `stack verify` command. Typing it prints the `stack` help and exits 0. Verification is part of `stack release` and runs the manifest's `verify` commands on the VM. Success prints the same two lines as a release with no plan (issue #122), so the failure path was used to prove it runs. The check was pointed at a string the site does not serve:

```
$ baryovm stack release bvm110-ctr --no-backup
✗ stack release: released but failed verification, the stack may need attention: remote `cd '/home/azureuser/bvm110-ctr' && curl -fsS http://127.0.0.1:8081/ | grep -q NOPE_NOT_THERE`: Process exited with status 1: exit status 1:
[exit 1]
```

With the real check restored the release passed again. The static site was also fetched from the local machine through the NSG (section 4), and the whoami service answered on the VM's loopback:

```
$ baryovm vm exec azure-x86 -- curl -sS http://127.0.0.1:8081/
Hostname: 7e2f2e38af17
IP: 127.0.0.1
...
```

## 7. Backup and restore

A table was created and one row inserted, a backup taken, then the row changed and another added.

```
$ baryovm stack backup bvm110-ctr
backup ok: db-20261020-125451.dump (4.0K) in /home/azureuser/bvm110-backups
✓ bvm110-ctr: stack backup done

$ baryovm stack restore bvm110-ctr
✗ baryovm: restore REPLACES the "bvm110-ctr" database: re-run with --yes to confirm
[exit 1]

$ baryovm stack restore bvm110-ctr --yes
restoring /home/azureuser/bvm110-backups/db-20261020-125451.dump
restored from /home/azureuser/bvm110-backups/db-20261020-125451.dump
✓ bvm110-ctr: stack restore done

$ baryovm vm exec azure-x86 -- docker exec bvm110-db psql -U postgres bvm110 -c "select * from t;"
       v       
---------------
 before-backup
(1 row)
```

With `--keep 3`, the backup directory held three dumps after four backups had run, so pruning works.

## 8. Tear down

BaryoVM has no command that stops a stack's containers (`stack remove` only forgets the registration), so the VM side was cleaned with `vm exec`:

```
$ baryovm vm exec azure-x86 -- docker compose -f /home/azureuser/bvm110-ctr/compose.yml down -v
 Container bvm110-whoami Removed
 Container bvm110-db Removed
 Network bvm110-ctr_default Removed
$ baryovm vm exec azure-x86 -- rm -rf /home/azureuser/bvm110-ctr /home/azureuser/bvm110-backups
$ baryovm vm exec azure-x86 -- sudo -n rm -rf /var/www/bvm110 /etc/nginx/conf.d/bvm110.conf
$ baryovm stack remove bvm110-site
✓ removed bvm110-site
$ baryovm stack remove bvm110-ctr
✓ removed bvm110-ctr
$ baryovm vm remove azure-x86
✓ removed azure-x86
```

`vm remove` leaves the VM host key in `known_hosts`. It only forgets the registration. Run `vm forget-key` first if you want it gone.

## Differences from the Oracle VM and from expected behaviour

- Login user is `azureuser`, not `opc`. `vm add` needs `--user azureuser`.
- Ubuntu takes the `apt-get` path in harden. It worked first time. sshd is 9.6, so `PerSourcePenalties` is skipped and harden says so.
- Bootstrap installs Docker only. nginx for a static site is a manual step (issue #18).
- `vm health` and `stack verify` are not commands, and an unknown subcommand exits 0 (issue #120).
- The first release of a stack that has a database fails on the pre-release backup (issue #121).
- A passing release does not say whether verify ran (issue #122).
- The `verify it` hint after a new host key passes `<the VM>:22` as the host. `ssh-keyscan` cannot resolve that (`getaddrinfo ... not known`); it needs `-p 22` and the bare host (PR #124).
- Nothing in the pass needed changes for x86_64 versus arm64.
