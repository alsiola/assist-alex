.PHONY: check install app test

check: test

test:
	$(MAKE) -C extension check
	swift test --package-path app

app:
	scripts/build-app.sh

install:
	scripts/install.sh
