# Local entry points for the checks CI runs (.github/workflows/ci.yml).
# Tool versions match CI; lint uses the golangci-lint on your PATH.

GOVULNCHECK_VERSION ?= v1.9.0
# The API codegen toolchain (make proto). protoc-gen-go must match the
# google.golang.org/protobuf version in go.mod, and protoc-gen-connect-go
# the connectrpc.com/connect version.
BUF_VERSION ?= v1.75.0
PROTOC_GEN_GO_VERSION ?= v1.36.12
PROTOC_GEN_CONNECT_GO_VERSION ?= v1.21.1

TOOLS := $(CURDIR)/.tools
export PATH := $(TOOLS):$(PATH)

.PHONY: all build test lint fmt tidy vuln proto proto-check ci

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
	GOBIN=$(TOOLS) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOBIN=$(TOOLS) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(TOOLS) go install connectrpc.com/connect/cmd/protoc-gen-connect-go@$(PROTOC_GEN_CONNECT_GO_VERSION)
	$(TOOLS)/buf lint
	$(TOOLS)/buf generate

# proto-check fails when the generated code is out of date.
proto-check: proto
	git add -N internal/api/gen
	git diff --exit-code -- internal/api/gen

ci: tidy proto-check lint test vuln
