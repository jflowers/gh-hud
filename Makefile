# Local entry points for the checks CI runs (.github/workflows/ci.yml).
# Tool versions match CI; lint uses the golangci-lint on your PATH.

GOVULNCHECK_VERSION ?= v1.9.0

.PHONY: all build test lint fmt tidy vuln ci

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

ci: tidy lint test vuln
