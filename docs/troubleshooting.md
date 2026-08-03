# Troubleshooting

k0smos has no shell or SSH. Start with the machine console.

For a local machine:

```bash
k0smosctl machine logs --name <machine> -f
```

Every PID 1 message starts with `k0smos:`. k0s writes to the same console after
it starts.

## The machine does not start

Run a dry run to inspect the resolved image, firmware, and QEMU arguments:

```bash
k0smosctl machine up --name test --dry-run
```

Common host-side causes are missing QEMU, missing UEFI firmware, an architecture
mismatch, or an API port already in use.

## The release cannot be downloaded

Pin a known release or use a local image:

```bash
k0smosctl machine up --release v1.36.3+k0s.0
k0smosctl machine up --image ./k0smos-metal-x86_64.qcow2
```

Set `GITHUB_TOKEN` or `GH_TOKEN` for private repositories or GitHub API rate
limits. A previously verified cached release remains usable when GitHub is
temporarily unavailable.

## The node does not become Ready

Look for these console stages in order:

1. the EROFS root is discovered and mounted read-only;
2. the `k0smos-data` partition is mounted at `/var`;
3. networking receives an address;
4. cloud-init or config-drive data is applied; and
5. k0smos starts `k0s controller` or `k0s worker`.

Useful messages:

| Console message | Meaning |
|---|---|
| `no module tree; assuming a monolithic kernel` | normal for the VM kernel |
| `kernel and modules are out of step` | the kernel and initramfs came from different builds |
| `metadata: could not read user-data` | the configuration drive was found but could not be applied |
| `UNSUPPORTED runcmd` | the supplied cloud-init requires a command k0smos deliberately does not execute |
| `warn: dhcp` | the interface did not receive a lease |
| `refusing to guess: N blank devices` | `k0smos.data=auto` found several possible data disks; identify one explicitly |

## Kubeconfig is not ready

`cluster kubeconfig` fails until k0s creates its admin configuration. For a
machine started manually, wait for the API server or increase `--timeout`:

```bash
k0smosctl cluster kubeconfig --name node-1 --timeout 30s -o kubeconfig
```

`cluster create` already waits for readiness and writes the kubeconfig itself.

## A machine will not shut down

Check that the console reached `listening for host commands` and that the named
machine's control socket still exists. Retry with:

```bash
k0smosctl machine shutdown --name <machine> --timeout 30s
```

Avoid deleting state or killing QEMU while it is writing `/var`.
