.PHONY: build build-runtime build-pkgx build-caddy build-ttyd clean runtime pkgx caddy ttyd

BUILDPACKS := runtime pkgx caddy ttyd
LDFLAGS := -s -w

build: $(BUILDPACKS)

$(BUILDPACKS):
	@echo "Building $@..."
	@cd $@ && GOOS=linux go build -ldflags="$(LDFLAGS)" -o ./bin/build ./run/main.go
	@cd $@ && GOOS=linux go build -ldflags="$(LDFLAGS)" -o ./bin/detect ./run/main.go

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