# Local entry points for the checks CI runs (.github/workflows/ci.yml).
# Tool versions match CI; lint uses the golangci-lint on your PATH.

GOVULNCHECK_VERSION ?= v1.9.0
# The API codegen toolchain (make proto). buf runs at this version; the Go
# plugins are tool directives in go.mod (go tool), so they always match the
# protobuf and connect libraries the code builds against.
BUF_VERSION ?= v1.75.0
BUF := go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

.PHONY: all build test lint fmt tidy vuln proto proto-check proto-breaking ci mac mac-notarize mac-check mac-icon client-fixtures android-sdk android android-install android-device-test android-dev-hub

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
	git add -N internal/api/gen macos/Sources/GHHudKit/Gen android/core/src/main/gen
	git diff --exit-code -- internal/api/gen macos/Sources/GHHudKit/Gen android/core/src/main/gen

ci: tidy proto-check lint test vuln

# The clients' shared fixtures (testdata/clients/README.md): the binary
# snapshots from their JSON, then what the views make of them, rendered by
# the Android app's code and checked by the Mac's.
client-fixtures:
	go test ./internal/api -run TestClientFixturesAreCurrent -update-fixtures
	cd android && UPDATE_FIXTURES=1 mise exec -- ./gradlew --console=plain :core:test --rerun-tasks
	cd macos && swift run GHHudChecks

# The Mac app (macos/). Needs only the Command Line Tools. MAC_SIGN_ID is a
# codesigning identity ("Developer ID Application: …"); empty signs ad hoc,
# which runs on this Mac only. MAC_NOTARY_PROFILE is a notarytool keychain
# profile (xcrun notarytool store-credentials) for mac-notarize; CI passes
# an App Store Connect API key in MAC_NOTARY_ARGS instead. MAC_ARCHS picks
# the architectures: empty builds this Mac's only (the Command Line Tools
# can't build more); a release passes "arm64 x86_64", which needs Xcode.
MAC_APP := build/gh-hud.app
MAC_SIGN_ID ?=
MAC_NOTARY_PROFILE ?= gh-hud-notary
MAC_NOTARY_ARGS ?= --keychain-profile "$(MAC_NOTARY_PROFILE)"
MAC_ARCHS ?=
MAC_VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
MAC_SWIFT_BUILD = cd macos && swift build -c release --product GHHud $(foreach a,$(MAC_ARCHS),--arch $(a))

mac:
	$(MAC_SWIFT_BUILD)
	rm -rf $(MAC_APP)
	mkdir -p $(MAC_APP)/Contents/MacOS $(MAC_APP)/Contents/Resources
	cp "$$($(MAC_SWIFT_BUILD) --show-bin-path)/GHHud" $(MAC_APP)/Contents/MacOS/GHHud
	cp macos/Resources/AppIcon.icns $(MAC_APP)/Contents/Resources/
	sed 's/VERSION/$(MAC_VERSION)/g' macos/Resources/Info.plist > $(MAC_APP)/Contents/Info.plist
	codesign --force --options runtime $(if $(MAC_SIGN_ID),--timestamp,--timestamp=none) --sign "$(if $(MAC_SIGN_ID),$(MAC_SIGN_ID),-)" $(MAC_APP)
	codesign --verify --strict $(MAC_APP)
	@echo "built $(MAC_APP)"

# mac-notarize builds with a Developer ID, has Apple notarize the app and
# staples the ticket, so it opens on any Mac. It leaves the stapled app
# zipped as build/gh-hud-app_VERSION.zip, a release's download.
MAC_ZIP = build/gh-hud-app_$(MAC_VERSION).zip
mac-notarize:
	@test -n "$(MAC_SIGN_ID)" || { echo "mac-notarize needs MAC_SIGN_ID (a Developer ID Application identity)"; exit 1; }
	$(MAKE) mac MAC_SIGN_ID="$(MAC_SIGN_ID)" MAC_VERSION="$(MAC_VERSION)" MAC_ARCHS="$(MAC_ARCHS)"
	rm -f build/gh-hud.zip $(MAC_ZIP)
	ditto -c -k --keepParent $(MAC_APP) build/gh-hud.zip
	xcrun notarytool submit build/gh-hud.zip $(MAC_NOTARY_ARGS) --wait
	xcrun stapler staple $(MAC_APP)
	spctl --assess --type execute -vv $(MAC_APP)
	rm -f build/gh-hud.zip
	ditto -c -k --keepParent $(MAC_APP) $(MAC_ZIP)
	@echo "notarized $(MAC_ZIP)"

mac-check:
	cd macos && swift run GHHudChecks

# The Android app (android/). The JDK and SDK tools come from mise.toml;
# Gradle, Kotlin and the SDK packages from the build. android-device-test
# needs a phone on adb (USB, or adb connect over the network).
GRADLE := cd android && mise exec -- ./gradlew --console=plain
# The SDK packages the build uses: adb, the platform it compiles against
# (compileSdk) and the Android Gradle Plugin's default build tools.
# Installing them also accepts the Android SDK license on this machine,
# which the build requires before it downloads anything itself.
ANDROID_SDK_PACKAGES := platform-tools platforms/android-37.0 build-tools/36.0.0
android-sdk:
	mise exec -- android sdk install $(ANDROID_SDK_PACKAGES)
android:
	$(GRADLE) lint testDebugUnitTest :core:test assembleDebug
android-install:
	$(GRADLE) installDebug
android-device-test:
	$(GRADLE) connectedDebugAndroidTest
# Points the app on a USB phone at this Mac's hub: forwards the hub's
# loopback port to the phone (adb reverse) and launches the app with the
# URL and token from hub.json. Run it again after the hub restarts (new
# port and token).
HUB_JSON ?= $(HOME)/.local/state/gh-hud/hub.json
android-dev-hub:
	@url=$$(jq -r .url "$(HUB_JSON)") && token=$$(jq -r .token "$(HUB_JSON)") && port=$${url##*:} && \
	mise exec -- adb reverse tcp:$$port tcp:$$port && \
	mise exec -- adb shell am start -n io.github.jflowers.ghhud/.MainActivity --es hub_url "$$url" --es hub_token "$$token" >/dev/null && \
	echo "the phone's app now uses $$url"

# mac-icon redraws macos/Resources/AppIcon.icns from make-icon.swift.
mac-icon:
	rm -rf build/AppIcon.iconset
	swift macos/Resources/make-icon.swift build/AppIcon.iconset
	iconutil -c icns build/AppIcon.iconset -o macos/Resources/AppIcon.icns
