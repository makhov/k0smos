# KubeVirt (and Cluster API via CAPK + k0smotron)

Boots and has been verified as a VM; the CAPI loop around it has **not** been
reconciled. Build the OCI artifacts:

```bash
ARCH=x86_64 make oci                              # for an amd64 cluster
PUSH=1 REGISTRY=ghcr.io/you TAG=v0 make oci
```

That produces one image, `k0smos:<tag>`, holding the kernel at `/boot/vmlinuz` and
the initramfs at `/boot/initramfs.gz`. The initramfs carries the immutable erofs
root, so the VM references the image once as its `kernelBoot` container; it does
not need a `containerDisk`.

One image rather than two because the kernel and the root cannot be versioned
independently: the root carries the module tree, so a mismatched pair produces the
skew k0smos reports at boot as "kernel and modules are out of step". It is also
pulled once instead of twice. `mkoci.sh` prints a matching VM spec when it
finishes, and `image/kubevirt-vm.yaml` is a worked example. The shape that
matters:

```yaml
firmware:
  kernelBoot:
    container:
      image: ghcr.io/you/k0smos:v0
      kernelPath: /boot/vmlinuz
      initrdPath: /boot/initramfs.gz
    kernelArgs: "console=ttyS0 k0smos.ip=dhcp k0smos.data=auto"
volumes:
  - name: data
    emptyDisk:
      capacity: 20Gi
```

**`k0smosctl`'s node commands do not reach a KubeVirt VM.** `kubeconfig`,
`shutdown` and `reboot` speak to a virtio-serial control port, which the local
QEMU runner attaches and a KubeVirt VM does not. There, shutdown is
`virtctl stop` (KubeVirt delivers ACPI, which k0smos watches for), and the
kubeconfig comes from wherever the cluster API server is reachable — or from the
disk offline. Attaching a port to a VMI spec would make them work, but nothing
here does that yet. `k0smosctl gen` is host-side and works anywhere, but Cluster
API generates the drive itself, so it is not needed there either.

A complete Cluster API manifest set is in
[`examples/capi-kubevirt.yaml`](https://github.com/makhov/k0smos/blob/main/examples/capi-kubevirt.yaml): `Cluster`,
`KubevirtCluster`, `K0sControlPlane`, `MachineDeployment`,
`K0sWorkerConfigTemplate` and both `KubevirtMachineTemplate`s. Field names come
from the providers' Go types, but the set has not yet been reconciled by a real
cluster — treat the first run as a test of the manifests too.

Three things that are easy to get wrong:

- **`virtualMachineBootstrapCheck.checkStrategy: none`.** CAPK defaults to `ssh`
  and reads the CAPI sentinel file over it. k0smos has no SSH, so without this
  machines never report bootstrapped even though the node joins.
- **CAPK attaches a config-drive, not NoCloud.** It uses
  `CloudInitConfigDrive`, so the drive is labelled `config-2` with the
  `openstack/latest/user_data` layout. k0smos handles both, and there is an e2e
  test for the config-drive path specifically because that is the one CAPI
  exercises.
- **Do not add a cloud-init volume yourself.** CAPK appends the volume and disk
  from the bootstrap Secret.
- **Attach a data volume** and pass `k0smos.data=auto`. The manifests use
  KubeVirt's `emptyDisk`, which shares the VMI's lifecycle: it survives a guest
  reboot and is discarded when the machine is replaced. That is what makes these
  nodes disposable without being diskless — kubelet gets a real filesystem, etcd
  survives an in-place reboot, and images are cached rather than re-pulled. Swap
  it for a `DataVolume`/PVC if you want it to outlive the machine.

Two more things to know:

- **Set `preInstalledK0s: true`** in the `K0sControllerConfig` /
  `K0sWorkerConfig` spec. k0smotron's `DownloadCommands` returns nothing when it
  is set, so no `curl`/`wget` commands are emitted — the image already ships k0s.
- **runcmd is interpreted, not executed** (see
  [Why runcmd is interpreted and never executed](../design/decisions.md#why-runcmd-is-interpreted-and-never-executed)).
  `k0s install <role>` becomes the
  supervised workload, `--force` is dropped, `--env KEY=VALUE` is applied to the
  child's environment, and anything else is logged `UNSUPPORTED`. Avoid
  `preK0sCommands`/`postK0sCommands` that assume a shell.

Deleting a VM raises an ACPI power button event, which k0smos honours by shutting
down cleanly, so no extra channel is needed.
