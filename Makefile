GO ?= go
NPM ?= npm
.DEFAULT_GOAL := build
VERSION ?= 0.1.0-dev
COMMIT := $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X gitee.com/kenny3755/gost_deploy/internal/cli.Version=$(VERSION) -X gitee.com/kenny3755/gost_deploy/internal/cli.Commit=$(COMMIT)
export GOTOOLCHAIN=local
.PHONY: build check test test-race fetch integration release clean web-build web-check web-test web-e2e
web/node_modules/.gost-deps: web/package.json web/package-lock.json
	$(NPM) --prefix web ci
	touch $@
web-build: web/node_modules/.gost-deps
	$(NPM) --prefix web run build
web-check: web/node_modules/.gost-deps
	$(NPM) --prefix web run check
	$(NPM) --prefix web run format:check
web-test: web/node_modules/.gost-deps
	$(NPM) --prefix web test
web-e2e: web-build
	$(NPM) --prefix web run test:e2e
build: web-build
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o bin/gostctl ./cmd/gostctl
	cd bin && sha256sum gostctl > SHA256SUMS
check: web-build
	$(NPM) --prefix web run format:check
	test -z "$$($(GO) fmt ./...)"
	$(GO) vet ./...
	bash -n install.sh tests/run-integration.sh tests/run-web-integration.sh
	git diff --check
test: web-build web-test
	$(GO) test ./...
test-race: web-build
	CGO_ENABLED=1 $(GO) test -race ./...
fetch:
	$(GO) run ./cmd/testctl fetch
integration: build fetch
	GO='$(GO)' bash tests/run-integration.sh
release: web-build
	@for arch in amd64 arm64; do \
	  dir="dist/gost-deploy_$(VERSION)_linux_$$arch"; \
	  mkdir -p "$$dir/bin" "$$dir/examples" "$$dir/docs"; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -buildvcs=false -ldflags '$(LDFLAGS)' -o "$$dir/bin/gostctl" ./cmd/gostctl || exit; \
	  cp install.sh README.md "$$dir/"; cp examples/profiles.json "$$dir/examples/"; cp docs/openapi.json "$$dir/docs/"; \
	  (cd "$$dir/bin" && sha256sum gostctl > SHA256SUMS); \
	  tar -C dist -czf "$$dir.tar.gz" "$${dir##*/}" || exit; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS
clean:
	rm -rf bin dist
