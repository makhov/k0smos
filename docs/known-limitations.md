# Known limitations

- **No upgrade path.** Rebuilding the disk image wipes the cluster. No A/B images.
- **No partition table and no grow-on-first-boot**, so one image fits any disk.
- **Multi-node.** The default workload is `k0s controller --single`. Roles and
  `--token-file` already pass through from cloud-init, but no multi-node cluster
  has been run, so treat it as unproven rather than supported.
- **`k0smosctl` talks to local guests only.** `kubeconfig`, `shutdown` and `reboot`
  use a virtio-serial control port that the QEMU runner attaches and a KubeVirt VMI
  does not. `gen` is host-side and works anywhere.
- **Cluster API has never been reconciled.** `examples/capi-kubevirt.yaml` was
  derived from the providers' Go types and never applied. The cloud-init contract
  it relies on is covered by e2e tests; the loop itself is not.
- **Physical hardware is untested.** The complete amd64 qcow2 has booted through
  OVMF/GRUB, found its GPT partitions, autoloaded drivers, mounted EROFS + `/var`,
  acquired DHCP and started k0s; the next evidence must come from real machines.
- **CI has never executed** — the repository has no remote.
- **Everything runs as root.** `/etc/passwd` exists only so k0s stops warning.

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
