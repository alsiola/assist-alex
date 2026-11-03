GO ?= go
PACKAGE_DIR ?= dist/assist
LDFLAGS ?= -s -w
# The package's version, from VERSION and git (see scripts/version.sh).
VERSION_SH := ../scripts/version.sh

.PHONY: check fmt-check vet test build package lint certify clean

check: fmt-check vet test build package lint

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

build:
	$(GO) build ./...

# The package directory, laid out as Open Conduit installs it.
package:
	$(eval VERSION := $(shell $(VERSION_SH)))
	@test -n "$(VERSION)" || { echo "can't work out the package version; see scripts/version.sh" >&2; exit 1; }
	rm -rf $(PACKAGE_DIR)
	mkdir -p $(PACKAGE_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS) -X github.com/alsiola/assist-alex/extension.Version=$(VERSION)' -o $(PACKAGE_DIR)/assist-extension ./cmd/assist-extension
	$(PACKAGE_DIR)/assist-extension package-manifest > $(PACKAGE_DIR)/open-conduit-extension.json
	$(PACKAGE_DIR)/assist-extension package-certification > $(PACKAGE_DIR)/certification.json
	cp -R playbooks $(PACKAGE_DIR)/playbooks

lint: package
	open-conduit extension check $(PACKAGE_DIR)

certify: package
	open-conduit extension certify $(PACKAGE_DIR)

clean:
	rm -rf dist
