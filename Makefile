.PHONY: build build-runtime build-pkgx build-caddy build-ttyd clean runtime pkgx caddy ttyd package publish publish-all

BUILDPACKS := runtime pkgx caddy ttyd
LDFLAGS := -s -w
TARGET_ARCH ?= amd64
IMAGE_NAME ?= docker.io/supervise/supervise-buildpack:latest

build: $(BUILDPACKS)

$(BUILDPACKS):
	@echo "Building $@ for linux/$(TARGET_ARCH)..."
	@cd $@ && GOOS=linux GOARCH=$(TARGET_ARCH) go build -ldflags="$(LDFLAGS)" -o ./bin/build ./run/main.go
	@cd $@ && GOOS=linux GOARCH=$(TARGET_ARCH) go build -ldflags="$(LDFLAGS)" -o ./bin/detect ./run/main.go

package: build
	@echo "Packaging buildpack for linux/$(TARGET_ARCH)..."
	@pack buildpack package $(IMAGE_NAME) --config package.toml --target linux/$(TARGET_ARCH)

publish: build
	@echo "Publishing buildpack for linux/$(TARGET_ARCH)..."
	@pack buildpack package $(IMAGE_NAME) --config package.toml --target linux/$(TARGET_ARCH) --publish

publish-all:
	@echo "Publishing for all architectures..."
	@$(MAKE) clean
	@$(MAKE) TARGET_ARCH=amd64 publish
	@$(MAKE) clean
	@$(MAKE) TARGET_ARCH=arm64 publish

build-runtime:
	@$(MAKE) runtime

build-pkgx:
	@$(MAKE) pkgx

build-caddy:
	@$(MAKE) caddy

build-ttyd:
	@$(MAKE) ttyd

clean:
	@for bp in $(BUILDPACKS); do \
		echo "Cleaning $$bp..."; \
		rm -f $$bp/bin/build $$bp/bin/detect; \
	done
