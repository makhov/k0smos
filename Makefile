BIN := dist/k0smos
# k0smos only runs as a linux init, so always cross-compile for the target —
# a host build on macOS would just produce the "linux only" stub.
GO_BUILD := GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build test vet kernel k0s initramfs disk boot smoke accept clean-dist
build:
	$(GO_BUILD) -o $(BIN) ./cmd/k0smos

test:
	go test -race ./...

vet:
	go vet ./...
	GOOS=linux go vet ./...

# --- local VM boot (works on macOS/Apple Silicon via HVF, and on linux/KVM) ---

# Alpine linux-virt kernel for the host arch. Needs docker to unpack the .apk.
kernel:
	./image/fetch-kernel.sh

# Latest k0s release binary for the host arch (~240MB).
k0s:
	./image/fetch-k0s.sh

# Initramfs with k0smos as /init. Set K0S_BIN=dist/k0s-<arch> to bake in k0s.
initramfs:
	./image/mkinitramfs.sh

# The real ext4 root that k0smos switch_roots onto. kubelet cannot run on the
# initramfs (cadvisor finds no filesystem info for a ramfs root).
disk: kernel k0s
	K0S_BIN=dist/k0s-$$(go env GOARCH) ./image/mkrootfs.sh dist/k0smos.img

# Full local boot: initramfs -> load modules -> switch_root onto the ext4 disk
# -> k0s. Interactive console.
boot: kernel disk
	./image/mkinitramfs.sh
	IMG=dist/k0smos.img MEM=8192 CPUS=4 ./image/run-qemu.sh

# Fast init-only check: no k0s, supervises /init (which exits 1 via the PID1
# gate) purely to prove the mount/cgroup/net/supervise/shutdown path works.
smoke: kernel initramfs
	EXEC=/init MEM=1024 ./image/run-qemu.sh

accept: disk
	./image/mkinitramfs.sh
	./image/acceptance.sh

clean-dist:
	rm -rf dist
