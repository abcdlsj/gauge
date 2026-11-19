VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# User-facing version: v0.2.0-3-gabc1234-dirty -> 0.2.0
SEMVER  := $(or $(shell printf '%s' '$(VERSION)' | sed -nE 's/^v?([0-9]+(\.[0-9]+)*).*/\1/p'), 0.0.0)
# Monotonic CFBundleVersion; git describe alone is not ordered.
BUILDNUM := $(or $(shell git rev-list --count HEAD 2>/dev/null), 1)
DESKTOP_LDFLAGS := -s -w -X main.version=$(VERSION)

DIST    := build/dist
APP     := build/bin/Gauge.app
APPICON := build/icon/AppIcon.icns
APKZIP  := $(DIST)/Gauge-$(SEMVER)-macos-universal.zip
DMGROOT := build/dmg
APKDMG  := $(DIST)/Gauge-$(SEMVER)-macos-universal.dmg

.PHONY: test vet fmt tidy clean icons app run-app dist version-check dmg

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

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

# The app icon is drawn by the script and committed (a .icns cannot be built
# without a rasteriser, and CI has none), so this only fills a missing file.
# Run `make icons` after changing the geometry. The tray PNGs the script writes
# are committed too, because main.go embeds them.
$(APPICON):
	python3 scripts/make_icons.py

icons:
	python3 scripts/make_icons.py

run-app: app
	open "$(APP)"

# A drag-to-install disk image: the bundle plus an /Applications symlink, so
# the mount window is the usual two-icon drop target. No Finder scripting and
# no background image, which keeps this reproducible on a headless runner.
dmg: app
	@mkdir -p "$(DIST)"
	@rm -rf "$(DMGROOT)"
	@mkdir -p "$(DMGROOT)"
	@ditto "$(APP)" "$(DMGROOT)/Gauge.app"
	@ln -s /Applications "$(DMGROOT)/Applications"
	@rm -f "$(APKDMG)"
	@hdiutil create -quiet -volname "Gauge $(SEMVER)" -srcfolder "$(DMGROOT)" \
		-fs HFS+ -format UDZO -ov "$(APKDMG)"
	@rm -rf "$(DMGROOT)"
	@echo "built $(APKDMG) ($$(du -h "$(APKDMG)" | cut -f1))"

# Release assets: the disk image and the zipped bundle.
dist: app dmg
	@mkdir -p "$(DIST)"
	@rm -f "$(APKZIP)"
	@ditto -c -k --keepParent "$(APP)" "$(APKZIP)"
	@echo "built $(APKZIP)"

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
	rm -rf build/dist build/dmg
