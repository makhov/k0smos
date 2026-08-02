# Operating it

**No shell, no SSH.** Plan for console access — serial, IPMI/SOL, or
`virsh console` — on every machine. That is your only live window.

**Diagnosis is offline.** Container logs never reach the console. Power the
machine down cleanly, then read the disk with `debugfs`; see
[Debugging](https://github.com/makhov/k0smos/blob/main/README.md#debugging).

**Data — treat every machine as disposable.** k0s state lives in `/var/lib/k0s`
on the root filesystem, and nothing is designed to outlive the machine:

- On **KubeVirt**, the erofs root carried in the initramfs is read-only and is
  never storage. Attach a data volume (`k0smos.data=auto`) and everything mutable
  lands there instead: with `emptyDisk` it survives a guest reboot and dies with
  the machine; with a `DataVolume`/PVC it outlives both.
- On **bare metal** the disk does persist across reboots, but Cluster API still
  replaces machines rather than repairing them, so do not rely on it.

Size the root for the container images a node will pull (`PAD_MB=`, default 3072
on top of the content). There is no A/B image scheme and no in-place upgrade:
roll machines instead.

**etcd membership is handled.** If you run the k0s control plane *on* k0smos
machines, every replacement is an etcd membership change — and because nothing
persists, a member that vanishes without leaving would sit in the member list
counting against quorum forever. k0smos therefore runs `k0s etcd leave` on
shutdown, while the controller is still up, before stopping anything. It is
skipped for workers and for `--single` (kine-backed) controllers, and a failure
is logged rather than blocking the shutdown: a cluster that has already lost
quorum cannot process the removal, and stopping is still correct.

**Kubernetes access.** The API server listens on 6443. The admin kubeconfig is
written inside the guest at `/var/lib/k0s/pki/admin.conf`; with no shell, the
practical way to retrieve it today is to power down and read it out of the image
with `debugfs`. Wiring that out properly is not done.

## Still missing for production

Roughly in the order worth fixing:

1. **A/B images and upgrades.** Nothing to roll forward or back today.
2. **Partition table and grow-on-first-boot**, so one image fits any disk.
3. **Multi-node.** The default workload is `k0s controller --single`. Roles and
   `--token-file` already pass through from cloud-init, but no multi-node cluster
   has been run, so treat it as unproven rather than supported.

Two entries have since been dealt with and are recorded here because the reasoning
matters:

- **Monolithic kernel** — done, and it is the default. Kata's guest kernel builds
  in virtio, ext4, overlayfs and netfilter, so the named-module class of failure
  does not arise. Hardware drivers on the modular path are autoloaded from
  `modules.alias` rather than named.
- **Config-drive / IMDS** — done for config-drive and NoCloud, including hostname
  and join tokens. Read in userspace, so it needs no kernel filesystem support.
  There is no IMDS client: CAPI attaches a drive, and nothing so far has needed
  the network path.
6. **A way to export the kubeconfig** that does not involve powering the machine
   off.
