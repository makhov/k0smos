# Using k0smos

- [When something goes wrong](#when-something-goes-wrong)

Throughout: k0smos has **no shell and no SSH**. Everything a machine needs is
supplied before it boots, and everything it reports comes out of the console.
That is the design, not a gap.

## When something goes wrong

The console is the only interface, and k0smos is deliberately talkative. Lines
worth recognising:

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

For e2e failures, guest consoles are saved to `dist/e2e/<test>.console.log`. They
are kept on failure only — set `K0SMOS_E2E_KEEP_CONSOLE=1` to keep them for
passing tests too, which is the only way to tell a passing assertion from one that
was skipped.
