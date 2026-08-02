# Operating it

**No shell, no SSH.** Plan for console access — serial, IPMI/SOL, or
`virsh console` — on every machine. That is your only live window.

**Diagnosis is offline.** Container logs never reach the console. Power the
machine down cleanly, then read the disk with `debugfs`; see
[Troubleshooting](../troubleshooting.md).

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
