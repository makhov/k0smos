# What you ship

What it takes to run k0smos somewhere other than the local QEMU setup, and what
is still missing for each target.

Read [Limitations](https://github.com/makhov/k0smos/blob/main/README.md#limitations) first. This is a working prototype;
the gaps below are real. For day-to-day use — booting, configuring, shipping
manifests, getting a kubeconfig — see [usage.md](../usage.md).

One artifact per platform, both wrapping the same immutable EROFS OS payload:

| Platform | Artifact |
|---|---|
| KubeVirt | one OCI kernelBoot image from `make oci` |
| Metal3 / bare metal | one UEFI-bootable qcow2 from `make metal` |

Kernel, initramfs and partition images remain internal build inputs. Machine
configuration is not baked into either artifact; Cluster API supplies it through
the provider's config-drive/user-data path.
