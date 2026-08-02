# The data volume

`/var/lib/k0s` — etcd, containerd, kubelet, pulled images — can live on a separate
volume, which is what lets a machine be disposable without being diskless:

```
k0smos.data=auto
```

Attach an ephemeral per-VM disk (KubeVirt `emptyDisk`) and it dies with the
machine; attach a PVC and it survives. Same image either way.

How `auto` picks, and why it is safe:

1. A volume already labelled `k0smos-data` is mounted as-is — the steady state
   after the first boot.
2. Otherwise it looks for a device with **no recognised filesystem**, so the root
   and the cloud-init drive can never be selected.
3. Exactly one blank device is formatted. Zero is not an error — k0s then uses the
   root filesystem. **More than one is refused**, not guessed at.

k0smos never formats a device that already has a filesystem.

The platform qcow2 already contains an ext4 `k0smos-data` partition mounted at
`/var`. Because `k0smosctl machine up` clones the complete artifact once per named
guest, etcd state and cached images survive a reboot without another flag.

An external data image is only part of the direct-kernel development path:

```bash
k0smosctl machine up --direct-kernel --data dist/data.img --data-size 8G
```

The image is created blank if it does not exist. Then on the guest side:

```
k0smos.data=auto
```

Other forms: `k0smos.data=/dev/vdb`, or `LABEL=`/`UUID=`. `k0smos.datadir=`
changes the mountpoint, `k0smos.datafstype=` the filesystem.
