BINARY  := gauge
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test vet fmt tidy clean size docker icons

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

APP := build/bin/Gauge.app
DESKTOP_LDFLAGS := -s -w -X main.version=$(VERSION)
APPICON := build/icon/AppIcon.icns

# macOS .app bundle. Wails needs its desktop tags plus the UTType framework on
# recent SDKs; the Info.plist makes macOS treat it as a regular GUI app.
app: $(APPICON)
	go build -trimpath -ldflags="$(DESKTOP_LDFLAGS)" \
		-o "$(APP)/Contents/MacOS/Gauge" ./cmd/gauge
	@mkdir -p "$(APP)/Contents/Resources"
	@cp packaging/Info.plist "$(APP)/Contents/Info.plist"
	@cp $(APPICON) "$(APP)/Contents/Resources/AppIcon.icns"
	@echo "built $(APP)"

# The app icon is drawn by the script; the tray PNGs it also writes are
# committed because main.go embeds them.
$(APPICON): scripts/make_icons.py
	python3 scripts/make_icons.py

icons:
	python3 scripts/make_icons.py

run-app: app
	open "$(APP)"

docker:
	docker build -t gauge:$(VERSION) .

clean:
	rm -rf bin
