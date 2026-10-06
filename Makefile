.PHONY: build test check dev dev-password

DEV_DB ?= data/dev.sqlite
DEV_PASSWORD_FILE ?= data/dev-owner-password
DEV_LISTEN ?= 127.0.0.1:8080
DEV_PUBLIC_URL ?= http://127.0.0.1:8080

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$$(git describe --always --dirty 2>/dev/null || echo dev)" -o bin/treetest ./cmd/treetest

test:
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs

check:
	go vet ./...
	CGO_ENABLED=1 go test -race ./...
	node --test web/tests/*.test.mjs

# The local database and password stay under ignored data/ by default. Never
# reset an existing owner's credentials just because the password file is gone.
dev: build
	@set -eu; umask 077; \
	  mkdir -p "$$(dirname "$(DEV_DB)")" "$$(dirname "$(DEV_PASSWORD_FILE)")"; \
	  if [ -e "$(DEV_DB)" ] && [ ! -f "$(DEV_PASSWORD_FILE)" ]; then \
	    echo 'Local database exists without its owner password file; refusing to reset the account.' >&2; exit 1; \
	  fi; \
	  if [ -f "$(DEV_PASSWORD_FILE)" ] && [ ! -e "$(DEV_DB)" ]; then \
	    echo 'Owner password file exists without its local database; refusing to reuse it.' >&2; exit 1; \
	  fi; \
	  if [ ! -f "$(DEV_PASSWORD_FILE)" ]; then \
	    output=$$(TREETEST_PASSWORD= ./bin/treetest user --db "$(DEV_DB)" --username owner --role owner); \
	    password=$$(printf '%s\n' "$$output" | sed -n 's/^Generated password: //p'); \
	    if [ -z "$$password" ]; then echo 'Could not capture the generated owner password.' >&2; exit 1; fi; \
	    printf '%s\n' "$$password" > "$(DEV_PASSWORD_FILE)"; \
	  fi; \
	  ./bin/treetest import --db "$(DEV_DB)" --study studies/example; \
	  printf '\nAdmin: %s/admin\nOwner password: ' "$(DEV_PUBLIC_URL)"; \
	  cat "$(DEV_PASSWORD_FILE)"; \
	  exec ./bin/treetest serve --db "$(DEV_DB)" --branding prometheus --listen "$(DEV_LISTEN)" --public-url "$(DEV_PUBLIC_URL)"

dev-password:
	@test -f "$(DEV_PASSWORD_FILE)" || { echo 'No local owner password yet; run make dev first.' >&2; exit 1; }
	@printf 'Owner password: '; cat "$(DEV_PASSWORD_FILE)"
