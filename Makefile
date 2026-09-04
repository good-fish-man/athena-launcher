VERSION ?= 1.1.1
MANIFEST_URL ?= https://github.com/good-fish-man/athena-launcher/releases/latest/download/release-manifest.json
DIST ?= dist
FRONTEND_PROJECT ?= ../frontend/agent-ui
GO_TOOLCHAIN ?= auto
LDFLAGS := -s -w -X athena-launcher/internal/launcher/deployment.LauncherVersion=$(VERSION) -X athena-launcher/internal/launcher/deployment.DefaultManifestURL=$(MANIFEST_URL)
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64
DESKTOP_TAGS := desktop,production,devtools
ifeq ($(shell uname -s),Linux)
DESKTOP_TAGS := desktop,production,devtools,webkit2_41
endif

.PHONY: test build frontend desktop desktop-app desktop-run release clean

test:
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go test ./...

build:
	@mkdir -p $(DIST)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o $(DIST)/athena-launcher ./cmd/athena-launcher

desktop:
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOTOOLCHAIN=$(GO_TOOLCHAIN) go build -tags "$(DESKTOP_TAGS)" -ldflags "$(LDFLAGS)" -o $(DIST)/athena-launcher-desktop ./cmd/athena-launcher

frontend:
	npm --prefix $(FRONTEND_PROJECT) run build

ifeq ($(shell uname -s),Darwin)
desktop-app: desktop
	BINARY="$(abspath $(DIST)/athena-launcher-desktop)" VERSION="$(VERSION)" APP_DIR="$(abspath $(DIST)/Athena.app)" ./packaging/macos/package-app.sh

desktop-run: frontend desktop-app
	@if [ -x "$(abspath $(DIST)/Athena.app/Contents/MacOS/athena-launcher)" ]; then \
		"$(abspath $(DIST)/Athena.app/Contents/MacOS/athena-launcher)" stop >/dev/null 2>&1 || true; \
	fi
	open -n -W "$(abspath $(DIST)/Athena.app)" --args --frontend-dir "$(abspath $(FRONTEND_PROJECT)/dist)"
else
desktop-app: desktop

desktop-run: frontend desktop-app
	./$(DIST)/athena-launcher-desktop --frontend-dir "$(abspath $(FRONTEND_PROJECT)/dist)"
endif

release:
	@VERSION=$(VERSION) DIST=$(DIST) MANIFEST_URL=$(MANIFEST_URL) ./scripts/build-launchers.sh

clean:
	rm -rf $(DIST)
