BIN := dist/k0smos
# k0smos only runs as a linux init, so always cross-compile for the target —
# a host build on macOS would just produce the "linux only" stub.
GO_BUILD := GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build ctl test vet kernel kernel-alpine k0s initramfs disk artifacts boot smoke oci e2e e2e-full accept clean-dist
build:
	$(GO_BUILD) -o $(BIN) ./cmd/k0smos

# k0smosctl runs on the host, not the node, so it builds for the host platform —
# unlike k0smos itself, which is always cross-compiled for linux.
ctl:
	go build -o dist/k0smosctl ./cmd/k0smosctl

test:
	go test -race ./...

vet:
	go vet ./...
	GOOS=linux go vet ./...

# --- local VM boot (works on macOS/Apple Silicon via HVF, and on linux/KVM) ---

# A Kata Containers guest kernel: monolithic, so no module tree, a ~1.2MB
# initramfs, no kernel/module version skew, and pinned by digest. This is the
# default because the target is VMs (KubeVirt, Cluster API), where it wins on
# every axis. Needs docker, for zstd.
#
# It was not always the default: it builds in no ISO9660, so it could not mount a
# cloud-init drive. internal/iso9660 reads those in userspace now, which removed
# the only objection.
kernel:
	./image/fetch-kernel-kata.sh

# Alpine linux-virt instead: modular, tracks Alpine rather than being pinned, and
# needs the ~29MB module tree to match the kernel exactly. Use it for bare metal,
# where Kata's guest kernel has no drivers at all — though note that broad
# hardware also wants modalias autoloading, which does not exist yet.
kernel-alpine:
	./image/fetch-kernel.sh

# Latest k0s release binary for the host arch (~240MB).
k0s:
	./image/fetch-k0s.sh

# Initramfs with k0smos as /init. Set K0S_BIN=dist/k0s-<arch> to bake in k0s.
#
# Rebuilding this ALONE is not enough after changing k0smos: switch_root re-execs
# /sbin/k0smos from the ext4 root, so everything after the pivot — cloud-init,
# data volume, supervise, shutdown — runs the binary in dist/k0smos.img. Rebuild
# `disk` too, or you will be testing stale code that boots perfectly.
initramfs:
	./image/mkinitramfs.sh

# The real ext4 root that k0smos switch_roots onto. kubelet cannot run on the
# initramfs (cadvisor finds no filesystem info for a ramfs root).
disk: kernel k0s
	K0S_BIN=dist/k0s-$$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.img

# Everything k0smosctl boot needs. `disk` already pulls in the kernel and k0s;
# initramfs is listed because it is built from the kernel's module tree and is not
# a prerequisite of the root image.
artifacts: kernel k0s initramfs disk

# Full local boot, through the CLI so there is one path a user can follow rather
# than a make-only shortcut that drifts from it.
boot: artifacts ctl
	./dist/k0smosctl boot --memory 8192 --cpus 4

# Fast init-only check: no k0s, supervises /init (which exits 1 via the PID1
# gate) purely to prove the mount/cgroup/net/supervise/shutdown path works.
smoke: kernel initramfs
	EXEC=/init MEM=1024 ./image/run-qemu.sh

# OCI artifacts for KubeVirt: a kernelBoot image (kernel + initramfs) and a
# containerDisk (the ext4 root). PUSH=1 to push, REGISTRY/TAG to retag.
# For a KubeVirt host, build amd64:
#   ARCH=x86_64 make oci
oci: kernel disk
	./image/mkinitramfs.sh
	./image/mkoci.sh

# End-to-end tests: boot k0smos under QEMU and assert on what happens. The fast
# suite never starts k0s (a workload that exits immediately), so each boot is
# ~40s; e2e-full adds the k0s tests, which take minutes each.
e2e: kernel initramfs disk
	go test -tags e2e -short -v -timeout 30m ./e2e/

e2e-full: kernel initramfs disk
	go test -tags e2e -v -timeout 90m ./e2e/

accept: disk
	./image/mkinitramfs.sh
	./image/acceptance.sh

clean-dist:
	rm -rf dist
