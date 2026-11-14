# Local entry points for the checks CI runs (.github/workflows/ci.yml).
# Tool versions match CI; lint uses the golangci-lint on your PATH.

GOVULNCHECK_VERSION ?= v1.9.0
# The API codegen toolchain (make proto). buf runs at this version; the Go
# plugins are tool directives in go.mod (go tool), so they always match the
# protobuf and connect libraries the code builds against.
BUF_VERSION ?= v1.75.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

.PHONY: all build test lint fmt tidy vuln proto proto-check proto-breaking ci mac mac-notarize mac-check

all: build

build:
	go build -o gh-hud ./cmd/gh-hud

test:
	go test -race -shuffle=on ./...

lint:
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

tidy:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# proto regenerates internal/api/gen from proto/ (after buf lint).
proto:
	$(BUF) lint
	$(BUF) generate

# proto-breaking fails when proto/ breaks clients built from
# BREAKING_AGAINST (a buf input: a directory, or .git#branch=NAME; by
# default your local master).
BREAKING_AGAINST ?= .git\#branch=master
proto-breaking:
	$(BUF) breaking --against '$(BREAKING_AGAINST)'

# proto-check fails when the generated code is out of date.
proto-check: proto
	git add -N internal/api/gen macos/Sources/GHHudKit/Gen
	git diff --exit-code -- internal/api/gen macos/Sources/GHHudKit/Gen

ci: tidy proto-check lint test vuln

# The Mac app (macos/). Needs only the Command Line Tools. MAC_SIGN_ID is a
# codesigning identity ("Developer ID Application: …"); empty signs ad hoc,
# which runs on this Mac only. MAC_NOTARY_PROFILE is a notarytool keychain
# profile (xcrun notarytool store-credentials) for mac-notarize.
MAC_APP := build/gh-hud.app
MAC_SIGN_ID ?=
MAC_NOTARY_PROFILE ?= gh-hud-notary
MAC_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

mac:
	cd macos && swift build -c release --product GHHud
	rm -rf $(MAC_APP)
	mkdir -p $(MAC_APP)/Contents/MacOS
	cp macos/.build/release/GHHud $(MAC_APP)/Contents/MacOS/GHHud
	sed 's/VERSION/$(MAC_VERSION)/g' macos/Resources/Info.plist > $(MAC_APP)/Contents/Info.plist
	codesign --force --options runtime $(if $(MAC_SIGN_ID),--timestamp,--timestamp=none) --sign "$(if $(MAC_SIGN_ID),$(MAC_SIGN_ID),-)" $(MAC_APP)
	codesign --verify --strict $(MAC_APP)
	@echo "built $(MAC_APP)"

# mac-notarize builds with a Developer ID, has Apple notarize the app and
# staples the ticket, so it opens on any Mac.
mac-notarize:
	@test -n "$(MAC_SIGN_ID)" || { echo "mac-notarize needs MAC_SIGN_ID (a Developer ID Application identity)"; exit 1; }
	$(MAKE) mac MAC_SIGN_ID="$(MAC_SIGN_ID)"
	rm -f build/gh-hud.zip
	ditto -c -k --keepParent $(MAC_APP) build/gh-hud.zip
	xcrun notarytool submit build/gh-hud.zip --keychain-profile "$(MAC_NOTARY_PROFILE)" --wait
	xcrun stapler staple $(MAC_APP)
	spctl --assess --type execute -vv $(MAC_APP)
	rm -f build/gh-hud.zip

mac-check:
	cd macos && swift run GHHudChecks
