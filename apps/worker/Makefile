SHELL := /bin/sh

IMAGE ?= ghcr.io/example/worm-worker:dev
WASM := dist/worm.component.wasm

.PHONY: test static-check package

test: static-check

static-check:
	./test/static-check.sh

# The component adapter/host is intentionally an explicit build input. The
# Lua script cannot acquire network access on its own; the adapter provides
# the narrowly scoped worm_host capability described in README.md.
package: $(WASM)
	docker build --file Containerfile --tag $(IMAGE) .

$(WASM):
	@echo "missing $(WASM): build the pinned Lua/WASI component adapter first" >&2
	@exit 1
