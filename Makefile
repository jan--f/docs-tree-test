.PHONY: build test check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$$(git describe --always --dirty 2>/dev/null || echo dev)" -o bin/treetest ./cmd/treetest

test:
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs

check:
	go vet ./...
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs
