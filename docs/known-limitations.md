# Support status

k0smos is an early-stage project. The table below separates implemented and
tested workflows from intended integrations.

| Area | Status | Evidence / gap |
|---|---|---|
| Local single-node cluster | Tested | created under QEMU and checked for node readiness in CI |
| Local multi-controller cluster | Tested | controllers join and reach Ready over the userspace guest network |
| Dedicated local workers | Partially tested | generation and role logic have unit coverage; a worker boot is not yet an e2e CI path |
| Immutable EROFS system | Tested | direct-kernel and UEFI boot paths verify the read-only root and writable `/var` |
| amd64 metal image | Firmware-tested | boots through OVMF, GRUB, GPT, EROFS, and clean shutdown |
| arm64 metal image | Built | release artifact is produced; equivalent firmware smoke coverage is pending |
| KubeVirt OCI image | Built | kernelBoot artifact and VM example exist; live KubeVirt validation is pending |
| Cluster API with KubeVirt | Experimental | manifests exist but have not completed reconciliation on a management cluster |
| Cluster API with Metal3 | Experimental | image reference exists; physical provisioning has not been validated |
| Physical hardware | Untested | driver coverage and firmware behavior require a real hardware matrix |
| Public-cloud images | Not implemented | no AMI, Azure image, GCP image, or provider metadata client |

## Product limitations

- There is no in-place, A/B, or API-driven OS upgrade. Roll out a new artifact
  and replace machines.
- There is no interactive shell or SSH escape hatch. Plan for serial console and
  Kubernetes-level observability.
- `k0smosctl` machine lifecycle and kubeconfig commands use a local QEMU control
  socket. They do not manage remote KubeVirt or physical machines.
- The cloud-init implementation is intentionally limited. Arbitrary `runcmd`
  shell commands are not executed.
- Everything inside the node runs as root; the image is minimized through
  immutability and the absence of general-purpose tools, not process-level user
  isolation.

Do not present an experimental integration as supported merely because its
artifact can be built. A platform becomes supported when its provisioning,
bootstrap, readiness, replacement, and clean shutdown paths are exercised end to
end.
