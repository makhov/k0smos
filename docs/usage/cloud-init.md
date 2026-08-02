# Configure a node with cloud-init

This is the whole configuration interface. `k0smosctl` builds the drive — it
writes the ISO itself, so there is no `xorriso` and no Docker involved:

```bash
make ctl

# put files on the node, taking their permissions from the source file
k0smosctl gen --file k0s.yaml:/etc/k0s/k0s.yaml --hostname demo-node -o dist/cidata.iso

k0smosctl machine up --cidata dist/cidata.iso
```

For a cloud-config you have written or rendered elsewhere, pass it whole
(`-` reads stdin):

```bash
k0smosctl gen --user-data cloud-config.yaml --hostname demo-node -o dist/cidata.iso
```

It checks what it generates with the same parser the node uses, so a drive that
would be ignored is refused here rather than booting into a machine that comes up
silently unconfigured. That catches malformed YAML, and also cloud-config missing
its `#cloud-config` first line — which k0smos ignores by design, so writing one
produces a drive with no effect. Nothing is written when it refuses.

Building the drive by hand still works if you prefer — the format is nothing
special:

```bash
xorriso -as mkisofs -V cidata -r -o dist/cidata.iso /tmp/cidata
```

`-r` (Rock Ridge) is required; `-J` (Joliet) is not.

Rock Ridge is what preserves the name `user-data`, whose hyphen is outside the
ISO9660 charset.

The drive is read **without being mounted** — k0smos parses the ISO itself — so no
kernel filesystem support is involved. An OpenStack config-drive works too:
label it `config-2` and use `openstack/latest/user_data` and
`openstack/latest/meta_data.json`.

## What is supported

**`write_files`** — `path`, `content`, `permissions`, and `encoding` of `b64`,
`gzip+base64` (or `gz+b64`), or plain. Parent directories are created. Bare
`gzip` without base64 is *not* supported: content arrives as a JSON/YAML string
and raw deflate bytes do not survive that.

**`meta-data`** — `local-hostname` sets the hostname, beating `k0smos.hostname=`
on the cmdline. `instance-id` is read and otherwise unused.

**`k0smos`** — an optional cloud-config section with `ip`, `iface`, `gateway`,
and `dns`. These have the same meanings as their `k0smos.*` kernel parameters and
override only the fields present. They are read before networking is configured.
`cluster create` uses this to give each clone a distinct address on its second
NIC without changing the shared artifact.

**`runcmd`** — **interpreted, never executed.** Nothing named in user-data is ever
exec'd. Four verbs are carried out with syscalls: `mkdir` (with `-p`), `chmod`,
`chown`, and `ln -s`. A `k0s install <role> …` is translated into the equivalent
foreground command, since k0smos supervises one process instead of registering a
systemd unit, and `--env KEY=VALUE` is lifted into the child's environment.
`systemctl` calls are dropped silently. Everything else — `curl`, `sed`, a script,
or any string containing `|`, `>`, `&&`, `$(…)` — is refused and logged as
`UNSUPPORTED runcmd`.

If a provider's user-data depends on something in that last category, the machine
will boot and tell you what it ignored. It will not half-apply it.
