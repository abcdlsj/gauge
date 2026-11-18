BINARY  := gauge
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# User-facing version: v0.2.0-3-gabc1234-dirty -> 0.2.0
SEMVER  := $(or $(shell printf '%s' '$(VERSION)' | sed -nE 's/^v?([0-9]+(\.[0-9]+)*).*/\1/p'), 0.0.0)
# Monotonic CFBundleVersion; git describe alone is not ordered.
BUILDNUM := $(or $(shell git rev-list --count HEAD 2>/dev/null), 1)
LDFLAGS := -s -w -X main.version=$(VERSION)
DESKTOP_LDFLAGS := -s -w -X main.version=$(VERSION)

DIST    := build/dist
APP     := build/bin/Gauge.app
APPICON := build/icon/AppIcon.icns
APKZIP  := $(DIST)/Gauge-$(SEMVER)-macos-universal.zip

# Headless builds. Windows only differs by the .exe suffix.
SERVER_TARGETS := linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: build run test vet fmt tidy clean size docker icons app run-app dist server-dist version-check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/gauge-server

run:
	go run ./cmd/gauge-server

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

size: build
	@ls -lh bin/$(BINARY) | awk '{print $$5, $$9}'

# macOS .app bundle, universal (arm64 + amd64). Both slices are built with cgo
# because the Wails tray/webview path wants the platform frameworks; lipo
# stitches them together and the result is ad-hoc signed so it runs unmodified
# from a DMG-less zip. The version comes from the git tag, not from the plist,
# so packaging/Info.plist does not need a bump on every release.
app: $(APPICON)
	@mkdir -p "$(APP)/Contents/MacOS" "$(APP)/Contents/Resources"
	CGO_ENABLED=1 GOARCH=arm64 go build -trimpath -ldflags="$(DESKTOP_LDFLAGS)" \
		-o "$(APP)/Contents/MacOS/Gauge.arm64" ./cmd/gauge
	CGO_ENABLED=1 GOARCH=amd64 go build -trimpath -ldflags="$(DESKTOP_LDFLAGS)" \
		-o "$(APP)/Contents/MacOS/Gauge.amd64" ./cmd/gauge
	@lipo -create -output "$(APP)/Contents/MacOS/Gauge" \
		"$(APP)/Contents/MacOS/Gauge.arm64" "$(APP)/Contents/MacOS/Gauge.amd64"
	@rm -f "$(APP)/Contents/MacOS/Gauge.arm64" "$(APP)/Contents/MacOS/Gauge.amd64"
	@cp packaging/Info.plist "$(APP)/Contents/Info.plist"
	@/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $(SEMVER)" \
		-c "Set :CFBundleVersion $(BUILDNUM)" \
		-c "Set :CFBundleIdentifier com.abcdlsj.gauge" "$(APP)/Contents/Info.plist"
	@cp $(APPICON) "$(APP)/Contents/Resources/AppIcon.icns"
	@codesign --force --deep --sign - "$(APP)"
	@echo "built $(APP) $(SEMVER) ($(BUILDNUM)) $(shell lipo -archs "$(APP)/Contents/MacOS/Gauge")"

# The app icon is drawn by the script; the tray PNGs it also writes are
# committed because main.go embeds them.
$(APPICON): scripts/make_icons.py
	python3 scripts/make_icons.py

icons:
	python3 scripts/make_icons.py

run-app: app
	open "$(APP)"

# Release assets: the universal app bundle and the headless server per platform.
dist: app
	@mkdir -p "$(DIST)"
	@rm -f "$(APKZIP)"
	@ditto -c -k --keepParent "$(APP)" "$(APKZIP)"
	@echo "built $(APKZIP)"

server-dist:
	@mkdir -p "$(DIST)"
	@set -e; for t in $(SERVER_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		if [ "$$os" = windows ]; then ext=.exe; fi; \
		out="$(DIST)/$(BINARY)-$(SEMVER)-$$os-$$arch$$ext"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags="$(LDFLAGS)" -o "$$out" ./cmd/gauge-server; \
	done

docker:
	docker build -t gauge:$(VERSION) .

# Everything that names a version must agree with the tag. The release workflow
# runs this before it builds anything; locally, `make version-check VERSION=v0.2.0`
# checks the same thing before you tag.
version-check:
	@v=$(SEMVER); \
	grep -q "CFBundleShortVersionString</key><string>$$v</string>" packaging/Info.plist || \
		{ echo "packaging/Info.plist is not $$v"; exit 1; }; \
	grep -q "\"productVersion\": \"$$v\"" wails.json || \
		{ echo "wails.json is not $$v"; exit 1; }; \
	command -v git >/dev/null && git rev-parse -q --verify "refs/tags/v$$v" >/dev/null && \
		echo "version $$v is consistent: tag, Info.plist, wails.json" || \
		echo "version $$v is consistent in the files (no tag v$$v yet)"

clean:
	rm -rf bin build/dist
