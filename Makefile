VERSION ?= 0.1.1
MANIFEST_URL ?=
DIST ?= dist
LDFLAGS := -s -w -X main.launcherVersion=$(VERSION) -X main.defaultManifestURL=$(MANIFEST_URL)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: test build release clean

test:
	GOTOOLCHAIN=local go test ./...

build:
	@mkdir -p $(DIST)
	GOTOOLCHAIN=local go build -ldflags "$(LDFLAGS)" -o $(DIST)/athena-launcher .

release:
	@VERSION=$(VERSION) DIST=$(DIST) MANIFEST_URL=$(MANIFEST_URL) ./scripts/build-launchers.sh

clean:
	rm -rf $(DIST)
