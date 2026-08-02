# KubeVirt

KubeVirt boots k0smos from a single OCI image using `kernelBoot`. The image
contains:

- `/boot/vmlinuz`
- `/boot/initramfs.gz`, including the immutable EROFS system

No `containerDisk` is required for the system. Attach a separate writable disk
for `/var`.

## Image reference

Published image tags are derived from the k0s release. Replace `+` in the GitHub
release tag with `-` and append the architecture:

```text
ghcr.io/makhov/k0smos:v1.36.3-k0s.0-amd64
```

## VirtualMachine shape

```yaml
spec:
  template:
    spec:
      domain:
        firmware:
          kernelBoot:
            container:
              image: ghcr.io/makhov/k0smos:v1.36.3-k0s.0-amd64
              kernelPath: /boot/vmlinuz
              initrdPath: /boot/initramfs.gz
            kernelArgs: >-
              console=ttyS0
              k0smos.ip=dhcp
              k0smos.data=auto
        devices:
          disks:
            - name: data
              disk:
                bus: virtio
      volumes:
        - name: data
          emptyDisk:
            capacity: 20Gi
```

Use a PVC or DataVolume instead of `emptyDisk` when `/var` must outlive the VMI.
The same OCI image works with either policy.

A complete example is available in
[`image/kubevirt-vm.yaml`](https://github.com/makhov/k0smos/blob/main/image/kubevirt-vm.yaml).

## Cluster API

The intended stack is Cluster API Provider KubeVirt (CAPK) with k0smotron
bootstrap and control-plane providers. The image already contains k0s, so set:

```yaml
spec:
  preInstalledK0s: true
```

CAPK must not wait for an SSH bootstrap sentinel because k0smos has no SSH:

```yaml
spec:
  virtualMachineBootstrapCheck:
    checkStrategy: none
```

k0smotron supplies the per-machine cloud-init config-drive. Do not add a second
cloud-init volume to the VM template.

The example set in
[`examples/capi-kubevirt.yaml`](https://github.com/makhov/k0smos/blob/main/examples/capi-kubevirt.yaml)
includes the cluster, control plane, machine deployments, and templates.

## Lifecycle

Use KubeVirt or Cluster API to stop and replace machines. KubeVirt delivers the
ACPI power event handled by k0smos. The local `k0smosctl` control socket is not
part of the current VMI manifest.
