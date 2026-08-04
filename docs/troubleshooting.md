# Troubleshooting

A k0smos machine has no shell and no SSH, so you ask it questions instead of
logging into it. Four commands cover almost everything:

```bash
k0smosctl machine logs   --name <machine> -f   # the console, as it happens
k0smosctl machine status --name <machine>      # what the init decided
k0smosctl machine dmesg  --name <machine>      # what the kernel saw
k0smosctl machine cat    --name <machine> <path>   # any file on the machine
```

`logs` shows the boot as it happens; every PID 1 message starts with `k0smos:`,
and k0s writes to the same console once it starts. `status` reports the same
conclusions after the fact, which is what you want when the interesting part has
already scrolled past.

## Start with status

```bash
$ k0smosctl machine status --name node-1
boot        2026-08-04T14:46:36Z
hostname    node-1
switched    true
workload    /usr/local/bin/k0s controller --single (running, restarts=0)

steps
  ok       mounts
  ok       modules                48 loaded, 6 autoloaded for 9 devices (6.18.35)
  ok       root                   LABEL=k0smos -> /dev/vda2
  ok       data                   /dev/vda3 -> /var
  ok       cgroup2
  ok       loopback
  ok       network                eth0 dhcp 10.0.2.15/24 gw 10.0.2.2
  ok       metadata               /dev/vdb (iso9660, LABEL=cidata)
```

Read it top to bottom: the first `FAILED` step is the thing to fix, and a high
`restarts` count with a `last exit` line means the machine booted but the
workload will not stay up.

Add `--json` to get the record as data. The same record is written inside the
machine at `/run/k0smos/boot.json`, so it is also readable with `machine cat`, or
off the disk if the machine never gets far enough to answer.

## The machine does not start

Nothing has booted yet, so this is a host-side problem. Inspect what would be
run:

```bash
k0smosctl machine up --name test --dry-run
```

Common causes: missing QEMU, missing UEFI firmware, an architecture mismatch, or
an API port already in use.

## The release cannot be downloaded

Pin a known release, or use a local image:

```bash
k0smosctl machine up --release v1.36.3+k0s.0
k0smosctl machine up --image ./k0smos-metal-x86_64.qcow2
```

Set `GITHUB_TOKEN` or `GH_TOKEN` for a private repository or to lift API rate
limits. An already-verified cached image stays usable when GitHub is unreachable.

## The machine boots but the node never becomes Ready

`status` will show the init finished, so the problem is in k0s. Its components
log to disk, not to the console, and `machine cat` is how you read them:

```bash
# what k0s was actually configured with
k0smosctl machine cat /etc/k0s/k0s.yaml

# a specific component
k0smosctl machine cat /var/log/pods/kube-system_kube-proxy-x2tbg/kube-proxy/0.log
```

Two failures worth recognising, because the message names the wrong component:

- **`iptables-restore` reporting `RULE_APPEND failed (No such file or directory)`**
  is a missing kernel module for a match or target, not a rules problem.
- **kube-router reporting `failed to synchronize cache: 1m0s timeout`** usually
  means kube-proxy programmed no rules, so the `kubernetes` service address never
  worked. Check kube-proxy first.

## Something hardware-related

Kernel messages never reach the console when `console=` is wrong or the failure
precedes PID 1, and on real hardware they are where driver, disk-controller and
firmware problems appear:

```bash
k0smosctl machine dmesg --name node-1 | grep -i -e virtio -e nvme -e eth
```

## Console messages worth knowing

| Console message | Meaning |
|---|---|
| `no module tree; assuming a monolithic kernel` | normal for the VM kernel |
| `kernel and modules are out of step` | the kernel and initramfs came from different builds; no modules were loaded |
| `metadata: could not read user-data` | the configuration drive was found but could not be applied |
| `UNSUPPORTED runcmd` | the cloud-init asks for a command k0smos deliberately does not execute |
| `warn: dhcp` | the interface did not get a lease |
| `refusing to guess: N blank devices` | `k0smos.data=auto` found several candidate disks; name one explicitly |

## Kubeconfig is not ready

`cluster kubeconfig` fails until k0s has created its admin configuration. For a
machine started on its own, wait for the API server or raise the timeout:

```bash
k0smosctl cluster kubeconfig --name node-1 --timeout 30s -o kubeconfig
```

`cluster create` already waits for readiness and writes the kubeconfig itself.

## A machine will not shut down

Check that the console reached `listening for host commands` and that the
machine's control socket still exists, then retry:

```bash
k0smosctl machine shutdown --name <machine> --timeout 30s
```

Avoid deleting state or killing QEMU while it is writing `/var`.

## On KubeVirt and bare metal

The commands above need the control port, which is attached by `k0smosctl` for
local machines. Elsewhere the serial console is the channel: the VMI console on
KubeVirt, serial-over-LAN or the BMC on bare metal. Everything PID 1 prints
appears there, and `/run/k0smos/boot.json` is readable from the disk.

## Access is a cluster-admin channel

Anything able to write to a machine's control port can read any file on it, fetch
its kubeconfig, and stop it. That is not a new exposure — the port is the
hypervisor's channel into the guest — but do not expose it anywhere the machine's
disk is not equally exposed.
