# Known limitations

- **No upgrade path.** Rebuilding the disk image wipes the cluster. No A/B images.
- **No partition table and no grow-on-first-boot.** Either would let one image
  fit any disk; neither exists yet.
- **Multi-node is exercised less than single-node.** `k0smosctl cluster create
  --controllers N --workers M` is a first-class command, and
  `e2e/cluster_test.go`'s `TestThreeControllerWorkerCluster` boots a
  three-controller etcd quorum that joins with a minted token and reaches
  Ready — the first test that exercises k0smos as more than one machine. The
  dedicated worker role is covered by unit tests (config generation,
  join-token placement) but not yet by an e2e boot, and there is one
  multi-node e2e test against four for the single-node path.
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
