GO ?= go
BIN := bin
MODULE := github.com/Duang777/drove
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: all build build-cli build-daemon test test-workspace-platforms vet lint fmt clean install help

all: build

## build: 编译 CLI 与 daemon 到 bin/
build: build-cli build-daemon

build-cli:
	$(GO) build -trimpath -ldflags "-s -w -X $(MODULE)/internal/version.Version=$(VERSION)" -o $(BIN)/drove ./cmd/drove

build-daemon:
	$(GO) build -trimpath -ldflags "-s -w -X $(MODULE)/internal/version.Version=$(VERSION)" -o $(BIN)/droved ./cmd/droved

## test: 运行全部单元测试（含 race）
test:
	$(GO) test ./... -race -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out | tail -n 1

## test-workspace-platforms: 编译 workspace 的全部平台实现与测试
test-workspace-platforms:
	GO=$(GO) scripts/check-workspace-platforms.sh

## vet: 静态检查
vet:
	$(GO) vet ./...

## lint: gofmt + go vet 快捷入口
lint: fmt vet

fmt:
	gofmt -l -w .

## install: 安装到 GOBIN
install: build
	install -m 0755 $(BIN)/drove $(GOBIN)/drove
	install -m 0755 $(BIN)/droved $(GOBIN)/droved

clean:
	rm -rf $(BIN) coverage.out

help:
	@grep -E '^## ' Makefile | sed 's/^## //'
