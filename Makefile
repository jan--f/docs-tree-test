.PHONY: build test check

build:
	CGO_ENABLED=0 go build -trimpath -o bin/treetest ./cmd/treetest

test:
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs

check:
	go vet ./...
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs
