BIN := dist/k0smos
GO_BUILD := CGO_ENABLED=0 go build -ldflags '-extldflags "-static"'

.PHONY: build test vet
build:
	$(GO_BUILD) -o $(BIN) ./cmd/k0smos

test:
	go test ./...

vet:
	go vet ./...
