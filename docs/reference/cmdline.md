# Kernel cmdline options

All optional; k0smos boots with defaults if none are given.

| Option | Default | Meaning |
|---|---|---|
| `k0smos.root=` | auto | Root override: `/dev/vda`, `UUID=…` or `LABEL=…`. By default PID1 uses its embedded root, then discovers `LABEL=k0smos`; `none` stays on the initramfs for smoke tests |
| `k0smos.rootfstype=` | `ext4` | Filesystem type fallback for a disk root; the detected type wins |
| `k0smos.rootflags=` | *(none)* | Mount data string, e.g. `noatime` |
| `k0smos.data=` | *(none)* | Data volume: `auto`, a device path, or `LABEL=`/`UUID=`. Unset disables it |
| `k0smos.datalabel=` | `k0smos-data` | Label applied when formatting, and searched for by `auto` |
| `k0smos.datafstype=` | `ext4` | Filesystem created and mounted |
| `k0smos.datadir=` | `/var` | Where the writable data volume is mounted |
| `k0smos.ip=` | *(none)* | `dhcp`, a static CIDR like `10.0.0.20/24`, or a per-interface list (below). Unset leaves loopback only |
| `k0smos.gw=` | *(none)* | Default gateway. Applies to `k0smos.iface` only — a machine has one default route |
| `k0smos.dns=` | *(none)* | Resolver for `/etc/resolv.conf`. **Overrides the DHCP lease** |
| `k0smos.iface=` | `eth0` | Interface a bare `k0smos.ip=` configures, and the one `k0smos.gw=` attaches to |
| `k0smos.hostname=` | `k0smos` | Hostname, also sent as the DHCP hostname |
| `k0smos.exec=` | `/usr/local/bin/k0s,controller,--single` | Supervised child, comma-separated (a cmdline value cannot contain spaces) |
| `k0smos.modules=` | *(built-in set)* | Comma-separated module list, or `+list` to add to the built-in set, or `none` to disable module loading entirely |
| `k0smos.path=` | see below | `PATH` exported to children |

`k0smos.path` defaults to
`/var/lib/k0s/bin:/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin`.
`/var/lib/k0s/bin` matters: k0s stages containerd, runc, kubelet and iptables
there at runtime.

## More than one interface

`k0smos.ip=` also takes a list of `interface:address` pairs, for a machine whose
management network is not the one the cluster talks over:

```
k0smos.ip=eth0:dhcp,eth1:10.10.0.11/24 k0smos.gw=10.0.2.2
```

Each entry is `dhcp` or a CIDR, applied in order. The gateway attaches to
`k0smos.iface` (`eth0` by default), so a second NIC on a segment with no router
needs nothing further. Tell kubelet which address is the node's — otherwise it
picks the one behind the default route, and every node in the cluster claims the
same one:

```
k0smos.exec=/usr/local/bin/k0s,controller,--enable-worker,--kubelet-extra-args=--node-ip=10.10.0.11
```

The built-in module set covers virtio, ext4, overlayfs, the netfilter and nft
pieces kube-proxy needs, ipsets, veth/bridge and the ACPI power button. Beyond it,
drivers are autoloaded by matching each device's `modalias` against
`modules.alias`, the same way udev does — so a distro kernel drives hardware
nothing named in advance. Modules absent from the kernel are skipped, so the same
list is safe on a monolithic kernel.

A list written with a leading `+` adds to the built-in set instead of replacing
it: `k0smos.modules=+netconsole` keeps every default and loads one module more.
Replacing the set means restating the drivers the machine needs for its own disk
and NIC, so prefer `+` for anything additive.

An image that carries extra drivers names the modules no device asks for in
`/etc/modules-load.d/*.conf` in its initramfs (the `modules-load.d(5)` format):
the NVIDIA image lists `nvidia_uvm` there. Those load alongside the built-in set
or a `k0smos.modules=` list, and not at all with `k0smos.modules=none`.

Module options come from the command line as `module.param=value`, as modprobe
takes them elsewhere: `kvm_amd.nested=1` loads `kvm_amd` with `nested=1`. Every
other argument -- `hugepagesz=2M hugepages=512`, `spec_rstack_overflow=safe-ret`
-- is the kernel's own and needs nothing from k0smos.
