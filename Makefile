VERSION ?= 0.1.3
MANIFEST_URL ?= https://github.com/good-fish-man/athena-launcher/releases/latest/download/release-manifest.json
DIST ?= dist
FRONTEND_PROJECT ?= ../frontend/agent-ui
LDFLAGS := -s -w -X main.launcherVersion=$(VERSION) -X main.defaultManifestURL=$(MANIFEST_URL)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64
DESKTOP_TAGS := desktop,production,devtools
ifeq ($(shell uname -s),Linux)
DESKTOP_TAGS := desktop,production,devtools,webkit2_41
endif

.PHONY: test build frontend desktop desktop-app desktop-run release clean

test:
	GOTOOLCHAIN=local go test ./...

build:
	@mkdir -p $(DIST)
	GOTOOLCHAIN=local go build -ldflags "$(LDFLAGS)" -o $(DIST)/athena-launcher .

desktop:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOTOOLCHAIN=local go build -tags "$(DESKTOP_TAGS)" -ldflags "$(LDFLAGS)" -o $(DIST)/athena-launcher-desktop .

frontend:
	npm --prefix $(FRONTEND_PROJECT) run build

ifeq ($(shell uname -s),Darwin)
desktop-app: desktop
	BINARY="$(abspath $(DIST)/athena-launcher-desktop)" VERSION="$(VERSION)" APP_DIR="$(abspath $(DIST)/Athena.app)" ./packaging/macos/package-app.sh

desktop-run: frontend desktop-app
	open -W "$(abspath $(DIST)/Athena.app)" --args --frontend-dir "$(abspath $(FRONTEND_PROJECT)/dist)"
else
desktop-app: desktop

desktop-run: frontend desktop-app
	./$(DIST)/athena-launcher-desktop --frontend-dir "$(abspath $(FRONTEND_PROJECT)/dist)"
endif

release:
	@VERSION=$(VERSION) DIST=$(DIST) MANIFEST_URL=$(MANIFEST_URL) ./scripts/build-launchers.sh

clean:
	rm -rf $(DIST)
