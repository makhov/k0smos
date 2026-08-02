# Troubleshooting

k0smos has **no shell and no SSH**. Everything a machine needs is supplied
before it boots, and everything it reports comes out of the console — that is
the design, not a gap. So there are two ways in: read the console, or read the
disk offline once the guest is down.

**Read the console.** Every init step is logged with a `k0smos:` prefix, and k0s
logs to the same console. `SERIAL=dist/console.log` captures it headlessly.
k0smos is deliberately talkative. Lines worth recognising:

| Line | Meaning |
|---|---|
| `no module tree; assuming a monolithic kernel` | Normal on the default kernel |
| `warn: /lib/modules has module trees […] but none for the running kernel` | Kernel and module tree are out of step — the images were built against a different kernel |
| `reading /dev/vdX (iso9660, LABEL=cidata) directly, no mount` | The cloud-init drive was found and is being parsed |
| `metadata: could not read user-data` | The drive was found but is unreadable — bootstrap data was **not** applied |
| `refusing to guess: N blank devices` | `k0smos.data=auto` with more than one candidate; name the device explicitly |
| `UNSUPPORTED runcmd […]` | A user-data command k0smos will not execute |
| `warn: dhcp on eth0` | No lease; the node has loopback only and cannot pull images |

Two failures that look like something else:

- **kube-router crashlooping with `failed to synchronize cache`** is usually *not*
  a kube-router problem. It means no service rules were programmed, so the
  `kubernetes` ClusterIP does not work and it cannot reach the API. Look for a
  missing netfilter module instead.
- **DNS timeouts under QEMU on macOS.** slirp's resolver never answers. Pass
  `k0smos.dns=1.1.1.1`; the local boot scripts already do.

## Container logs are missing from the console

Container logs never reach the console, but the root is a raw ext4 file, so
`debugfs` reads it without mounting or root (Docker because `debugfs` is not on
macOS):

```bash
docker run --rm -v "$PWD/dist:/d" alpine:3.20 sh -c '
  apk add -q --no-cache e2fsprogs e2fsprogs-extra >/dev/null
  debugfs -R "ls /var/log/pods" /d/k0smos.img'
```

> **Shut the guest down cleanly first.** Killing QEMU leaves `Block bitmap
> checksum does not match`, which loses recent writes and makes directories read
> as empty — a diagnosis will silently mislead you.

## e2e failures

For e2e failures, guest consoles are saved to `dist/e2e/<test>.console.log`. They
are kept on failure only — set `K0SMOS_E2E_KEEP_CONSOLE=1` to keep them for
passing tests too, which is the only way to tell a passing assertion from one that
was skipped.
