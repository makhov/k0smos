BIN := dist/k0smos
TARGET_MACHINE := $(if $(ARCH),$(ARCH),$(shell uname -m))
TARGET_GOARCH := $(if $(filter arm64 aarch64,$(TARGET_MACHINE)),arm64,amd64)
TARGET_APKARCH := $(if $(filter arm64 aarch64,$(TARGET_MACHINE)),aarch64,x86_64)
# k0smos only runs as a linux init, so always cross-compile for the target —
# a host build on macOS would just produce the "linux only" stub.
GO_BUILD := GOOS=linux GOARCH=$(TARGET_GOARCH) CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build ctl test vet kernel kernel-alpine kernel-metal k0s initramfs root rootfs disk artifacts verify-artifacts metal e2e-artifacts boot smoke oci e2e e2e-full accept clean-dist
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

# Alpine linux-lts for physical machines: unlike linux-virt it includes the
# broad NIC, storage, USB and platform driver set reached by modalias autoloading.
kernel-metal:
	KERNEL_PACKAGE=linux-lts KERNEL_ROOT=dist/kernel-metal ./image/fetch-kernel.sh

# Latest k0s release binary for the host arch (~240MB).
k0s:
	./image/fetch-k0s.sh

# Initramfs with k0smos as /init. Set K0S_BIN=dist/k0s-<arch> to bake in k0s.
#
# Rebuilding this ALONE is not enough after changing k0smos, and with an embedded
# root it is doubly not enough. There are two copies of the binary: /init here, and
# /sbin/k0smos inside the root image — which switch_root re-execs, so everything
# after the pivot runs that one. The root image is inside this initramfs, so a
# `make initramfs` on its own refreshes /init and silently keeps the old
# /sbin/k0smos.
#
# Use `make artifacts`, which rebuilds the root first and then embeds it. Getting
# this wrong produces a boot that works perfectly while testing code that is gone —
# it cost a whole e2e run to notice the second time.
initramfs:
	./image/mkinitramfs.sh

# The canonical, platform-independent OS payload. Platform-specific kernel
# modules belong in the initramfs wrapper, never in this image, so KubeVirt and
# Metal3 can prove they carry the same root bytes.
root: k0s
	ROOTFS=erofs MODULES_DIR=dist/no-platform-modules \
		K0S_BIN=dist/k0s-$(TARGET_GOARCH) ./image/mkrootfs.sh dist/k0smos.erofs

# The root k0smos switch_roots onto in kernel-matrix tests. kubelet cannot run on the initramfs itself
# (cadvisor finds no filesystem info for a ramfs root), but it is satisfied by a
# read-only erofs image on a loop device — which is why the root can travel inside
# the initramfs rather than as a separate disk.
# No kernel prerequisite: the root only needs a kernel for its module tree, and
# which kernel that is belongs to the caller — `artifacts` fetches the default,
# while CI picks per matrix leg. mkrootfs.sh reports a missing tree as monolithic.
rootfs: k0s
	K0S_BIN=dist/k0s-$$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.erofs

# The writable ext4 root used by most of the e2e suite. Platform releases use
# the canonical EROFS root instead.
disk: k0s
	ROOTFS=ext4 K0S_BIN=dist/k0s-$$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.img

# KubeVirt kernelBoot inputs. Keep root before initramfs: the latter embeds the
# former byte-for-byte. Local `k0smosctl machine up` consumes the `metal` artifact.
artifacts: kernel k0s root initramfs

# Guard the invariant that makes the single-image KubeVirt packaging work: the
# initramfs must contain the exact immutable root built alongside it. This also
# catches the easy-to-make rootfs/initramfs ordering mistake in release jobs.
verify-artifacts: artifacts
	./image/verify-artifacts.sh

# The single Cluster API/Metal3-facing artifact. Internally this assembles a raw
# GPT disk first, but only the bootable qcow2 is the product users consume.
metal: kernel-metal k0s root
	./image/build-metal.sh

# Everything the e2e suite boots: the default node, plus the ext4 disk most of the
# tests switch onto. No kernel prerequisite, so a caller can choose one first — CI
# does, per matrix leg. Defined here rather than spelled out in the workflow,
# because the last time those two drifted CI built an erofs image named
# k0smos.img and every test that boots it by LABEL waited out its timeout.
e2e-artifacts: k0s rootfs initramfs disk

# Full local boot consumes the same single firmware artifact shipped to Metal3.
# --attach because a contributor running this wants to watch the boot; ctrl-c
# then stops the guest cleanly.
boot: metal ctl
	./dist/k0smosctl machine up --image dist/k0smos-metal-$(TARGET_APKARCH).qcow2 \
		--attach --memory 8192 --cpus 4

# Fast init-only check: no k0s, supervises /init (which exits 1 via the PID1
# gate) purely to prove the mount/cgroup/net/supervise/shutdown path works.
smoke: kernel initramfs
	EXEC=/init MEM=1024 ./image/run-qemu.sh

# OCI artifact for KubeVirt: one kernelBoot image containing the kernel and an
# initramfs which itself contains the immutable erofs root. PUSH=1 to push,
# REGISTRY/TAG to retag.
# For a KubeVirt host, build amd64:
#   ARCH=x86_64 make oci
oci: artifacts
	./image/verify-artifacts.sh
	./image/mkoci.sh

# End-to-end tests: boot k0smos under QEMU and assert on what happens. The fast
# suite never starts k0s (a workload that exits immediately), so each boot is
# ~40s; e2e-full adds the k0s tests, which take minutes each.
e2e: kernel e2e-artifacts
	go test -tags e2e -short -v -timeout 30m ./e2e/

e2e-full: kernel e2e-artifacts
	go test -tags e2e -v -timeout 90m ./e2e/

accept: disk
	./image/mkinitramfs.sh
	./image/acceptance.sh

clean-dist:
	rm -rf dist

# Documentation. The real targets live in docs/Makefile, matching how k0s and
# k0smotron lay this out; these delegate so the root stays the entry point.
.PHONY: docs docs-serve
docs:
	$(MAKE) -C docs docs

docs-serve-dev:
	$(MAKE) -C docs serve-dev
