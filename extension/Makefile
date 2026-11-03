GO ?= go
PACKAGE_DIR ?= dist/assist
LDFLAGS ?= -s -w

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
	rm -rf $(PACKAGE_DIR)
	mkdir -p $(PACKAGE_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(PACKAGE_DIR)/assist-extension ./cmd/assist-extension
	$(PACKAGE_DIR)/assist-extension package-manifest > $(PACKAGE_DIR)/open-conduit-extension.json
	$(PACKAGE_DIR)/assist-extension package-certification > $(PACKAGE_DIR)/certification.json
	cp -R playbooks $(PACKAGE_DIR)/playbooks

lint: package
	open-conduit extension check $(PACKAGE_DIR)

certify: package
	open-conduit extension certify $(PACKAGE_DIR)

clean:
	rm -rf dist
