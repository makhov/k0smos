# Boot a node locally

Needs Go 1.25+ to build the host CLI, plus QEMU. Docker is required only when
building an OS artifact locally. Works on Apple Silicon via HVF and on Linux/KVM.

To consume a release, build only the host CLI:

```bash
make ctl
```

Then boot with the CLI:

```bash
k0smosctl machine up
guest "default" running in the background (pid 7547)
  console:  k0smosctl machine logs -f
  cluster:  k0smosctl cluster kubeconfig -o kubeconfig   (API on :6443)
  stop it:  k0smosctl machine shutdown
```

**The guest runs in the background and the command returns.** A k0smos node has no
shell, so there is nothing to sit in front of. Its console goes to a file you read
with `k0smosctl machine logs` — `-f` follows it, `-n 50` shows the last fifty lines. Give it
a minute or two; `k0s controller --single` has a lot to start.

`--attach` stays in the foreground streaming the console, and there **ctrl-c shuts
the guest down cleanly** rather than killing it. (`--interactive` hands the terminal
to QEMU, whose only escape is `ctrl-a x` — which kills the guest. It exists for the
rare case of wanting the QEMU monitor; prefer `--attach`.)

Useful flags: `--release` pins a GitHub tag, `--cache-dir` relocates downloaded
artifacts, `--image` selects a local qcow2 without contacting GitHub, `--cidata`
attaches a configuration drive, and `--dry-run` prints the QEMU command instead
of running it. The artifact already contains its writable `/var` partition.

`machine up` uses the same verified release cache automatically:

```bash
k0smosctl machine up --arch amd64

# Or use a local build directly.
k0smosctl machine up --image dist/k0smos-metal-x86_64.qcow2 --arch amd64
```

Separate `--kernel`, `--initramfs`, `--disk`, `--data`, `--exec` and `--no-image`
belong to `--direct-kernel`, the low-level development and smoke-test path.

## Guests, and where their state lives

Each guest has a `--name` (default `default`) and its own directory under
`~/.local/state/k0smos/<name>/` — root disk, console log, control socket and a
little metadata. Nothing runtime is written into the working tree, so
`make clean-dist` cannot take a running machine's socket with it. `K0SMOS_STATE_DIR`
moves it elsewhere.

**The platform image is a template.** `machine up` clones it into the guest's directory the
first time and never writes to the image itself, which is what makes a second guest
one command:

```bash
k0smosctl machine up --name vm2 --api-port 7443
k0smosctl cluster kubeconfig --name vm2 -o kubeconfig2   # port comes from the machine
```

Machine lifecycle and cluster access commands take `--name`:

```bash
k0smosctl machine list                     # what exists, and what is running
k0smosctl machine logs -f --name vm2
k0smosctl machine shutdown --name vm2
k0smosctl machine rm --name vm2            # discard it; the next up re-clones the image
```

A later `machine up` of the same name reuses that guest's disk, so a reboot keeps the
cluster. `machine rm` throws it away, which is how these nodes are meant to be treated —
replaced rather than repaired.

This matters more than convenience. Booting the image in place would allow only one
guest per machine, and any copy taken afterwards would inherit that guest's cluster
identity: k0s writes its PKI on first boot, so two clones of a booted image come up
with the same CA and the same node UID. Cloning per guest makes that impossible.

While iterating on k0smos itself, the fast path skips k0s entirely and takes about
15 seconds:

```bash
make smoke
```

**If you change any k0smos code, rebuild the complete artifact.** After `switch_root`,
k0smos re-execs `/sbin/k0smos` from the EROFS root, so everything after the pivot
runs the binary in `dist/k0smos.erofs`, not `/init` in the initramfs:

```bash
make metal
```

Use the target rather than the scripts. There are two copies of the k0smos binary
in the build inputs — `/init`, and `/sbin/k0smos` inside the
root image, which is the one `switch_root` re-execs — and `mkinitramfs.sh` on its own
refreshes only the first. `make artifacts` rebuilds the root and then embeds it, in
that order.

Rebuilding only the initramfs tests stale code that boots perfectly. It cost real
debugging time to notice.
