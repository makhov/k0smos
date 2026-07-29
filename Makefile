BIN := dist/k0smos
# k0smos only runs as a linux init, so always cross-compile for the target —
# a host build on macOS would just produce the "linux only" stub.
GO_BUILD := GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build test vet image accept
build:
	$(GO_BUILD) -o $(BIN) ./cmd/k0smos

test:
	go test ./...

vet:
	go vet ./...
	GOOS=linux go vet ./...

image: build
	chmod +x image/*.sh
	./image/mkrootfs.sh dist/k0smos.img

# Requires a linux host with QEMU, K0S_BIN and KERNEL set.
accept: image
	./image/acceptance.sh dist/k0smos.img
