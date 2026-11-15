# NVIDIA GPUs

GPU nodes use a variant of the metal image that carries NVIDIA's data center
driver. It is built for x86_64, for H100, H200 and B200, from one pinned
NVIDIA package per driver branch.

Every release ships both branches, so picking a driver is a download rather
than a build: `k0smos-metal-nvidia-620.70.06-x86_64.qcow2` and
`k0smos-metal-nvidia-580.196.07-x86_64.qcow2`.

To build one yourself, for another branch or another kernel:

```bash
ARCH=x86_64 make metal-nvidia NVIDIA_VERSION=620.70.06   # R620
ARCH=x86_64 make metal-nvidia NVIDIA_VERSION=580.196.07  # R580
```

The modules are compiled against the exact kernel build pinned as
`KERNEL_RELEASE` in the `Makefile`, which is why that pin exists: an image is
a kernel and the modules built for it, and nothing else reproduces one. A
driver branch other than those two needs its package digest added to
`image/fetch-nvidia.sh` first.

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
| `/dev/nvidia-caps/*`, the MIG capabilities | created by k0smos at the same time |

The libraries must be exactly the version of the kernel module, so they come
with the image. Only compute and utility are included: no OpenCL, video
(NVENC/NVDEC) or graphics libraries.

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

## MIG

Multi-Instance GPU works, but there is no shell to run `nvidia-smi` in, so the
partitioning is set from a pod. The GPU Operator's MIG manager does it from a
node label, which is the usual way on any Kubernetes node:

```bash
kubectl label node <node> nvidia.com/mig.config=all-1g.10gb --overwrite
```

A process needs a capability node, `/dev/nvidia-caps/nvidia-cap<N>`, to
partition a GPU, to monitor one, or to use a single instance. The driver does
not create these; a distro has its libraries call setuid `nvidia-modprobe`
each time one is missing, and the image carries neither that nor udev. So
k0smos creates them itself at boot, from the driver's own table in
`/proc/driver/nvidia-caps/`. That table is fixed — it lists every instance the
driver could ever have, not the ones that exist — so an instance created hours
later already has its node.

MIG mode survives a reboot; the instance geometry does not. The MIG manager
recreates it from the label when the node comes back.

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
          image: ghcr.io/makhov/k0smos:dev-nvidia-620.70.06
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
        image: ghcr.io/makhov/k0smos:dev-nvidia-620.70.06
    - name: data
      emptyDisk:
        capacity: 100Gi
```

EFI rather than BIOS because H100, H200 and B200 have memory windows larger
than BIOS maps.
