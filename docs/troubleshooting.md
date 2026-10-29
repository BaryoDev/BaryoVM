# Troubleshooting

Organised by what you see. Error text marked "from sudo" or "from the Go SSH library" is not
BaryoVM's own and can vary by version.

> Not yet run against a fresh VM. The `read key`, `parse key` and `connection refused` outputs were
> produced by running the binary locally. The host key texts are quoted from the source
> (`internal/hostkeys/hostkeys.go`).

## `read key ...: permission denied`

```text
vm ping: read key /tmp/bvm-k1: open /tmp/bvm-k1: permission denied
```

Your user cannot read the key file. Fix it:

```sh
chmod 600 ~/.ssh/id_ed25519
```

BaryoVM reads the key itself and does not check its mode. A mode that is too open is refused by
`ssh` and `rsync`, not by this tool, so `chmod 600` it anyway.

## `read key ...: no such file or directory`

```text
vm ping: read key /tmp/nokey: open /tmp/nokey: no such file or directory
```

The path stored by `vm add` is wrong. `vm add` does not read the key when you pass `--os`, so it
saves a bad path without complaint. Run `vm add` again with the right `--key`.

## `parse key ...`

```text
vm ping: parse key /tmp/badkey: ssh: no key found
```

The file is not a private key (a `.pub` file, or an empty one). A key with a passphrase fails here
too, with `ssh: this private key is passphrase protected` (from the Go SSH library). BaryoVM does
not use `ssh-agent`, so use a key without a passphrase, such as a dedicated deploy key.

## `ssh dial host:port: ... connection refused`, or a timeout

```text
vm ping: ssh dial 127.0.0.1:2: dial tcp 127.0.0.1:2: connect: connection refused
```

Nothing answers on that host and port. A firewall that drops packets shows as an `i/o timeout`
after 15 seconds. Check the address, then the port.

## Non-22 SSH port

`vm add` defaults to port 22. If sshd listens elsewhere:

```sh
baryovm vm add myvm --host <ip> --port 2222 --user ubuntu --key ~/.ssh/id_ed25519
```

The port is stored with the VM, and `stack release` and `site deploy` pass it to rsync. Host keys
are recorded per `host:port`, which is why the host key messages below show `203.0.113.10:2222`
for such a VM. To change the port of a registered VM, run `vm add` again with `--port`.

## `unable to authenticate`

```text
ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain
```

From the Go SSH library. The server refused the key. Check that the public half is in the VM
user's `~/.ssh/authorized_keys`, that `--user` is right, and that `--key` is the matching private
key.

## `sudo: a password is required`

Commands that need root run `sudo -n`, which never prompts. If the user needs a password for sudo,
the command fails with sudo's own text, wrapped by BaryoVM as
``remote `<command>`: Process exited with status 1: sudo: a password is required``. Give the SSH
user passwordless sudo:

```sh
echo 'deploy ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/deploy
sudo chmod 440 /etc/sudoers.d/deploy
```

Release hooks run `sudo -n "$SHELL" -c ...`. A sudoers line that allows `NOPASSWD` for single
programs only is not enough there, and gives the same "a password is required", which reads as a
password problem and is a permissions one.

## `sudo: sorry, you must have a tty to run sudo`

From sudo. The host sets `requiretty` in sudoers. BaryoVM does not allocate a terminal, so sudo
refuses. Remove the setting for the SSH user:

```sh
echo 'Defaults:deploy !requiretty' | sudo tee /etc/sudoers.d/deploy-notty
sudo chmod 440 /etc/sudoers.d/deploy-notty
```

## `permission denied` from Docker

The SSH user is not in the `docker` group. Either add it (`sudo usermod -aG docker deploy`, then
connect again), or register the stack with `--sudo` so its commands run through `sudo -n`.

## `host key for <host:port> changed`

```text
host key for 203.0.113.10:22 changed
  stored   SHA256:aWxF...
  offered  SHA256:9KpQ...
```

A rebuilt VM is the ordinary reason, and so is someone answering on that address who should not
be. The tool cannot tell, so it stops. If you rebuilt the machine:

```sh
baryovm vm forget-key myvm
```

The next connection learns the new key and prints its fingerprint, with an `ssh-keyscan` line to
check it.

## `no recorded host key for <host>, and strict host key checking is on`

You passed `--strict-host-keys` (or set `BARYOVM_STRICT_HOST_KEYS=1`) for a host the tool has not
seen. The message carries the offered fingerprint and a command to record it:

```text
no recorded host key for 203.0.113.10:22, and strict host key checking is on.
  offered  SHA256:...
...
  ssh-keyscan -t ecdsa,ed25519 203.0.113.10 >> ~/.baryovm/known_hosts
```

Compare the fingerprint to the machine, then run that line. Or connect once without the flag to
learn the key. `--strict-host-keys=false` turns strict mode off for one command.

## `no recorded host key for <host:port> in <path>`

Printed by `vm forget-key` when that host has no key recorded. Check the name, and the port if it
is not 22.

## `project release`: a VM has no pinned host key

```text
vms[0].hostKey: vm "client-vm" has no pinned host key, and a project release never trusts a host
on first use: add its public key to the file ...
```

Add `hostKey` to the project file (`ssh-keyscan -t ed25519 <host>`, then check the fingerprint), or
pass `--strict-host-keys` with the key already in `~/.baryovm/known_hosts`.

## `vm add` warns `could not reach ... to detect its OS`

The VM is saved and read as Linux. It is a warning, not an error. Run `vm add` again once the host
is up. A host key mismatch is different: `vm add` fails and saves nothing.

## Still stuck

Run the failing command with `-o json` and open an issue with the `error` field. Do not paste
keys, `.env` files or an unredacted `fleet.json`.
