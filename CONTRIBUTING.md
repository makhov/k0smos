# Contributing

Building k0smos from source. To *use* k0smos, download a release instead — see
the [documentation](https://makhov.github.io/k0smos/).

## Requirements

- Go 1.25 or newer
- QEMU with UEFI firmware
- Docker, for the Linux image-building tools (also needed on macOS)

## Build

```bash
make ctl         # host CLI into dist/k0smosctl
make artifacts   # kernel, initramfs and EROFS root
make metal       # UEFI qcow2 and raw disk
make oci         # KubeVirt OCI image
```

`make artifacts` downloads the kernel and the k0s binary on first use.

## Test

```bash
make test        # unit tests
make vet         # vet for the host and for GOOS=linux
make e2e         # QEMU boot tests
make e2e-full    # k0s readiness and multi-node tests
```

Guest consoles from failed e2e runs are kept under `dist/e2e/*.console.log`.
Set `K0SMOS_E2E_KEEP_CONSOLE=1` to keep them from passing runs too.

## Run what you built

The CLI takes a local artifact instead of resolving a release:

```bash
./dist/k0smosctl cluster create --name dev --image dist/k0smos-metal-x86_64.qcow2
```

For the low-level path — separate kernel and initramfs, no platform artifact:

```bash
./dist/k0smosctl machine up --direct-kernel \
  --kernel dist/kernel/x86_64/vmlinuz \
  --initramfs dist/k0smos-initramfs.gz
```

## Layout

```text
cmd/k0smos/       Linux PID 1 and boot lifecycle
cmd/k0smosctl/    host CLI for local machines and clusters
internal/         boot, configuration, networking and shutdown components
image/            kernels, root filesystem and platform packaging
e2e/              QEMU-based acceptance tests
examples/         Cluster API manifests
docs/             documentation site (mkdocs)
```

## Docs

```bash
make docs         # build the site
make docs-serve-dev   # serve it locally in a container
```

Pages under `docs/` are user-facing: they describe using a released
`k0smosctl` and released artifacts. Keep build instructions, test status and
development workflow here instead.

`docs/reference/k0smosctl.md` mirrors the CLI's own help but is maintained by
hand, so update it in the same change that adds or renames a flag.

## Internals

Design notes live in the site under
[Internals](https://makhov.github.io/k0smos/design/boot-chain/) — the boot chain
and the reasoning behind the less obvious decisions.
