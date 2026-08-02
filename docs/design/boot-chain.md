# The boot chain

Why the boot sequence is ordered the way it is. Nearly every step exists to
prevent a specific failure that was observed on a real boot; the ordering looks
arbitrary until you know what each one is for.

This is the *why*. For how to use the thing, see [usage.md](../usage.md); for
running it on KubeVirt or Cluster API, [deployment/kubevirt.md](../deployment/kubevirt.md).

```mermaid
graph TD
  A[kernel] --> B[initramfs /init]
  B --> C[mount /proc /sys /dev]
  C --> D[load modules]
  D --> E[find root by LABEL/UUID]
  E --> F[switch_root]
  F --> G[PID1 supervises k0s]
```

```
firmware/QEMU/KubeVirt
  └── kernel (Kata guest kernel by default, Alpine linux-virt, or your own)
        └── initramfs: k0smos as /init          ← PID1, pre-switch
              ├── mount /proc /sys /dev /run /tmp …
              ├── read /proc/cmdline
              ├── export PATH                    (mkfs and k0s both need it)
              ├── load modules: named set, then autoload by modalias
              ├── choose root: explicit override → embedded EROFS → LABEL=k0smos
              ├── mount it at /newroot
              └── switch_root ── exec /sbin/k0smos --switched-root
                    └── k0smos as /sbin/k0smos   ← PID1, post-switch
                          ├── mount anything that did not come across
                          ├── load modules (again; harmless if already in)
                          ├── prepare the data volume → /var/lib/k0s
                          ├── set up cgroup2
                          ├── loopback up
                          ├── network: DHCP or static, write /etc/resolv.conf
                          ├── read the cloud-init drive (no mount)
                          │     ├── write_files → syscalls
                          │     ├── runcmd → interpreted, never executed
                          │     └── meta-data may supply the hostname
                          ├── set hostname
                          ├── install SIGCHLD reaper
                          ├── watch control port + power button
                          ├── supervise the workload
                          │     (k0s controller --single, unless user-data
                          │      names a role and join token)
                          └── on shutdown request:
                                k0s etcd leave (controllers, not --single)
                                killall TERM → grace → KILL
                                sync → unmount → sync → remount / ro
                                reboot(2)
```

Two orderings in there are load-bearing and easy to get backwards: the data volume
is mounted before anything can write to `/var/lib/k0s`, and the cloud-init drive is
read after networking (so an HTTP source could later work) but before the hostname
is set (so it can supply one).
