# NVIDIA GPUs

GPU nodes use a variant of the metal image that carries NVIDIA's data center
driver. It is built for x86_64, for H100, H200 and B200, from one pinned
NVIDIA package per driver branch:

```bash
ARCH=x86_64 make metal-nvidia NVIDIA_VERSION=620.70.06   # R620
ARCH=x86_64 make metal-nvidia NVIDIA_VERSION=580.196.07  # R580
```

The output is `dist/k0smos-metal-nvidia-<version>-x86_64.qcow2`. It boots a
machine without a GPU like the plain metal image does.

For KubeVirt, `ARCH=x86_64 make oci-nvidia NVIDIA_VERSION=...` builds the
`kernelBoot` image `k0smos:<tag>-nvidia-<version>` (see [below](#kubevirt)).

## What the image provides

The image takes the place of the GPU Operator's driver container:

| Part | Where |
|---|---|
| Open GPU kernel modules: `nvidia`, `nvidia_uvm`, `nvidia_peermem` | initramfs, loaded at boot |
| GSP firmware | initramfs, carried to `/lib/firmware` on the root |
| `libcuda`, NVML, `nvidia-smi` and the glibc they run on | its own partition, mounted read-only at `/run/nvidia/driver` |
| `/dev/nvidia*`, `/dev/nvidia-uvm` | created by k0smos once the driver has found the GPUs |

The libraries must be exactly the version of the kernel module, so they come
with the image. Only compute and utility are included: no OpenCL, video
(NVENC/NVDEC) or graphics libraries. There are no `/dev/nvidia-caps` nodes, so
MIG partitioning is not available.

On NVSwitch systems (HGX), Fabric Manager runs on the host that passes the GPUs
through, not on the k0smos guest.

## GPU Operator

Install the operator with its driver disabled. Its toolkit configures k0s's
containerd as on any k0s node, with its own files on `/var`, because `/usr` is
read-only:

```bash
helm install gpu-operator nvidia/gpu-operator -n gpu-operator --create-namespace \
  --set driver.enabled=false \
  --set toolkit.installDir=/var/lib/nvidia-toolkit \
  --set toolkit.env[0].name=CONTAINERD_CONFIG \
  --set toolkit.env[0].value=/etc/k0s/containerd.d/nvidia.toml \
  --set toolkit.env[1].name=CONTAINERD_SOCKET \
  --set toolkit.env[1].value=/run/k0s/containerd.sock \
  --set toolkit.env[2].name=CONTAINERD_RUNTIME_CLASS \
  --set toolkit.env[2].value=nvidia
```

`hostPaths.driverInstallDir` keeps its default, `/run/nvidia/driver`, which is
where k0smos mounts the driver. `/etc` is rebuilt at each boot, and the toolkit
writes its containerd file again whenever its pod starts.

## KubeVirt

The usual KubeVirt image boots Kata's kernel, which cannot load modules, so GPU
VMs use `k0smos:<tag>-nvidia-<version>`: the metal kernel, an initramfs with the
modules, firmware and root, and the driver root at `/disk/k0smos.img`. The same
image is the `kernelBoot` container and a `containerDisk`:

```yaml
spec:
  domain:
    firmware:
      bootloader:
        efi:
          secureBoot: false # the kernel and modules are not signed
      kernelBoot:
        container:
          image: ghcr.io/amakhov/k0smos:dev-nvidia-620.70.06
          kernelPath: /boot/vmlinuz
          initrdPath: /boot/initramfs.gz
        kernelArgs: "console=ttyS0 k0smos.ip=dhcp k0smos.data=auto"
    devices:
      disks:
        - name: nvidia-driver
          disk: {bus: virtio}
        - name: data
          disk: {bus: virtio}
      gpus:
        - name: gpu0
          deviceName: nvidia.com/GH100_H100_SXM5_80GB # as permittedHostDevices names it
  volumes:
    - name: nvidia-driver
      containerDisk:
        image: ghcr.io/amakhov/k0smos:dev-nvidia-620.70.06
    - name: data
      emptyDisk:
        capacity: 100Gi
```

EFI rather than BIOS because H100, H200 and B200 have memory windows larger
than BIOS maps.
