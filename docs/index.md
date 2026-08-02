# k0smos

A minimal Go PID1 for running [k0s](https://k0sproject.io) Kubernetes nodes. No
shell, no busybox, no systemd — the image contains k0smos, k0s, and a handful of
libraries.

k0smos does the OS init a Kubernetes node needs and nothing else: mount
pseudo-filesystems, load kernel modules, switch onto a real root, prepare a data
volume, configure networking, read its bootstrap data off a cloud-init drive, then
supervise one workload for the life of the machine and shut it down cleanly when
asked.

There is no SSH and no shell to get into. A machine is configured entirely by
cloud-init — which is what Cluster API providers already produce — and replaced
rather than administered. `runcmd` is **interpreted, never executed**.

- **[Installation](install/quick-start.md)** — build the artifacts and boot a node.
- **[Usage](usage/cli.md)** — the CLI, cloud-init, clusters, and day-to-day operation.
- **[Design](design/boot-chain.md)** — why the boot sequence is ordered the way it is.
