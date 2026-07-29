BIN := dist/k0smos
# k0smos only runs as a linux init, so always cross-compile for the target —
# a host build on macOS would just produce the "linux only" stub.
GO_BUILD := GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build test vet kernel k0s initramfs boot smoke image accept clean-dist
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

# Full local boot: k0smos as PID1 supervising k0s. Interactive console.
boot: kernel k0s
	K0S_BIN=dist/k0s-$$(go env GOARCH) ./image/mkinitramfs.sh
	MEM=8192 CPUS=4 ./image/run-qemu.sh

# Fast init-only check: no k0s, supervises /init (which exits 1 via the PID1
# gate) purely to prove the mount/cgroup/net/supervise/shutdown path works.
smoke: kernel initramfs
	EXEC=/init MEM=1024 ./image/run-qemu.sh

# --- persistent-disk path (needs a kernel with virtio_blk + ext4 BUILT IN) ---

image: build
	./image/mkrootfs.sh dist/k0smos.img

accept: image
	./image/acceptance.sh dist/k0smos.img

clean-dist:
	rm -rf dist
