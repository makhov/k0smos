# Building your own image

Every released image is built by this repository's `Makefile`, and so is
yours. The versions that go into an image -- k0s, the kernel, the NVIDIA
driver -- are build variables, not something baked into the source, so
changing one is a variable on the command line rather than a patch.

You need `git`, `docker` and `make`. Nothing else: each step that needs a
Linux tool (`mkfs.erofs`, `grub-install`, `depmod`, the NVIDIA module
compiler) runs in a container the build starts for itself.

The repository has to sit on a **case-sensitive filesystem**, which rules out
a stock macOS checkout for anything that carries a kernel module tree: Linux
ships `xt_DSCP.ko` and `xt_dscp.ko` as different modules, and they overwrite
each other on APFS. `make kernel-metal` checks this and says so. Build on
Linux, or make a case-sensitive APFS volume in Disk Utility and clone there.

```bash
git clone https://github.com/makhov/k0smos && cd k0smos
make metal                    # dist/k0smos-metal-<arch>.qcow2
```

## What you can change

| Variable | Default | What it does |
|---|---|---|
| `ARCH` | the host's | `x86_64` or `aarch64`. Images are not cross-arch; build one per architecture. |
| `K0S_VERSION` | the latest k0s release | The k0s the image ships, e.g. `v1.37.2+k0s.1`. A k0smos release is named after this. |
| `KERNEL_RELEASE` | `7.0.0-43-generic` | The exact Ubuntu kernel build for the metal images. Empty takes whatever `linux-image-generic` currently resolves to. |
| `UBUNTU_RELEASE` | `26.04` | The Ubuntu release that kernel comes from. |
| `NVIDIA_VERSION` | `620.70.06` | The GPU driver branch ([GPUs](gpus.md)). |
| `NVIDIA_SHA256` | the two pinned branches | The driver package's digest, needed only for a version this repo has no digest for. |
| `LIBC` | `glibc` | The C library on the root, `glibc` or `musl` ([below](#the-c-library-on-the-root)). |
| `ROOT_TOOLS` | `/usr/bin/mount /usr/bin/umount /usr/sbin/mkfs.ext4 /usr/sbin/mkfs.xfs` | The user-space helpers on a glibc root, as absolute paths in the build container. k0s and kubelet need these; add to the list rather than replacing it. |
| `APK_PKGS` | `mount umount ca-certificates-bundle e2fsprogs xfsprogs` | The same, for `LIBC=musl`, as Alpine package names. |
| `FIRECRACKER_DIR` | unset | A directory with `firecracker` and `jailer` to include, for a node that runs sandboxed workloads. Released images carry no VMM. |
| `METAL_DATA_MB` | `8192` | The data partition's size in the built image. It grows into a bigger disk on first boot, so this is a floor, not a ceiling. |
| `METAL_KERNEL_ARGS` | console, data volume and DHCP | The command line baked into the image's GRUB config ([Kernel parameters](../reference/cmdline.md)). |
| `REGISTRY`, `TAG`, `PUSH` | `ghcr.io/amakhov`, `dev`, off | Where `make oci` and `make oci-nvidia` publish the KubeVirt artifact. |

Each variable goes on the `make` command line or in the environment:

```bash
ARCH=x86_64 K0S_VERSION=v1.37.1+k0s.0 make metal
ARCH=x86_64 KERNEL_RELEASE=7.0.0-41-generic make metal
ARCH=x86_64 NVIDIA_VERSION=570.195.03 NVIDIA_SHA256=<digest> make metal-nvidia
ARCH=x86_64 LIBC=musl make build metal
```

## The C library on the root

Four binaries on the root link against a C library at all — `mount`, `umount`,
`mkfs.ext4` and `mkfs.xfs`. k0smos itself, k0s, and everything k0s stages at
runtime are static Go and need none. So `LIBC` does not decide what k0smos
needs; it decides what *else* can run on a node.

The default is glibc, because software built for an ordinary distro expects it.
A container runtime that ships its own CRIU, for instance, will install a
reduced static build on a musl root and a full one on glibc. Released images are
glibc.

`LIBC=musl` assembles the Alpine-based root that k0smos carried before, which is
a few megabytes smaller. The two are close enough in size that it is not a
reason to choose; pick musl only if you want the smaller surface and nothing on
the node needs glibc.

Neither root has a shell, and neither has a package manager. On glibc the
helpers are copied in with their libraries, because a `.deb`'s maintainer
scripts would need a shell; on musl they come from the narrow Alpine
subpackages, for the same reason. This is also why `ROOT_TOOLS` takes paths and
`APK_PKGS` takes package names.

Note that `LIBC` has nothing to do with `make kernel-alpine`, which is about
whose *kernel* a test image boots.

A built image says what went into it, so a machine booted from it can be
asked rather than guessed at -- see [Which image is this?](operating.md#which-image-is-this).

## The targets

| Target | Output |
|---|---|
| `make metal` | the bootable qcow2 for bare metal, Metal3 and local VMs |
| `make metal-nvidia` | the same with NVIDIA's driver, x86_64 only |
| `make oci` | the KubeVirt `kernelBoot` image |
| `make oci-nvidia` | the same with NVIDIA's driver |
| `make artifacts` | kernel, root and initramfs separately, for custom packaging |
| `make ctl` | `k0smosctl` for the host |

## When a pin goes stale

`KERNEL_RELEASE` names one Ubuntu build, and Ubuntu drops a build from the
archive once the release moves past it. The fetch then fails and says so,
rather than quietly building a different kernel than last time. Bump it to a
build the archive still has:

```bash
make kernel-metal KERNEL_RELEASE=
```

takes the current one and prints the release it got; put that in the
`Makefile`. A GPU image must then be rebuilt against the new kernel, because
its modules are compiled for one exact build and load for no other.
