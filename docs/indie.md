# Quickstart: one cheap VM, one app

For a $5 VM and a side project. You end with a container running on your VM, deployed from your
laptop. No fleets, no CI.

> Not yet run on a fresh VM. Every command here exists in the current binary and its flags were
> checked against `--help`, but the output has not been copied from a real run.

You need:

- A Linux VM you can SSH into with a key, and a user on it that has `sudo` without a password.
- The key on your machine, for example `~/.ssh/id_ed25519`.
- `baryovm` installed (see [Install](../README.md#install)).

## 1. Register the VM

```sh
baryovm vm add myvm --host <vm-ip> --user ubuntu --key ~/.ssh/id_ed25519
baryovm vm ping myvm
```

`vm add` connects once to detect the OS and records the host key in `~/.baryovm/known_hosts`.
`vm ping` reports the host and the Docker version. If either fails, see
[troubleshooting](troubleshooting.md).

## 2. Install Docker

```sh
baryovm vm bootstrap myvm
```

It does nothing if Docker is already there. Add `--dry-run` to read the script first.

## 3. Put a compose file on the VM

`stack` drives a compose project that already lives on the VM. Write a small one with `vm exec`:

```sh
baryovm vm exec myvm -- sh -c 'mkdir -p ~/hello && printf "services:\n  web:\n    image: nginx:alpine\n    ports:\n      - \"8080:80\"\n" > ~/hello/compose.yaml'
```

## 4. Register and deploy it

`--path` is absolute, so use your VM user's home directory:

```sh
baryovm stack add hello --vm myvm --path /home/ubuntu/hello
baryovm stack deploy hello
baryovm stack ps hello
```

`stack deploy hello --dry-run` prints the compose command without connecting. Run locally, it prints:

```text
stack deploy (dry run)
  1. compose up: cd '/home/ubuntu/hello' && docker compose up -d
```

## 5. Check that it answers

```sh
baryovm vm exec myvm -- curl -fsS http://127.0.0.1:8080/
```

You should get the nginx welcome page. To reach it from a browser, open port 8080 in your
provider's firewall.

## Done

You have a registered VM, a registered stack and a running container. Next:

- Logs: `baryovm stack logs hello --tail 50`
- Give it a health check: `baryovm stack set-update hello --health-url http://127.0.0.1:8080/`
- Ship your own source with `stack release`, which syncs, builds on the VM and verifies. See
  [USAGE.md](../USAGE.md#release-sync-source-build-deploy-config-driven).
- If the app has a Postgres container, add backups: [SME page](sme.md).
