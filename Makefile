# MacPanel — 构建与开发

# 版本信息注入
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -s -w \
	-X github.com/lancelot-lewis/MacPanel/internal/version.Version=$(VERSION) \
	-X github.com/lancelot-lewis/MacPanel/internal/version.Commit=$(COMMIT) \
	-X github.com/lancelot-lewis/MacPanel/internal/version.BuildTime=$(BUILD_TIME)

BIN_DIR := bin
BINARY  := $(BIN_DIR)/macpanel

# Go 可能由 mise 管理，若 PATH 中找不到则回退到 mise shim。
GO := $(shell command -v go 2>/dev/null || echo $(HOME)/.local/share/mise/shims/go)
NPM := npm

.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps: ## 安装前后端依赖
	$(GO) mod download
	cd web && $(NPM) install

.PHONY: web-install
web-install: ## 只安装前端依赖
	cd web && $(NPM) install

.PHONY: web-build
web-build: ## 构建前端产物到 web/dist
	cd web && $(NPM) run build

.PHONY: web-dev
web-dev: ## 前端开发服务器（需后端已在 7788 运行）
	cd web && $(NPM) run dev

.PHONY: web-typecheck
web-typecheck: ## 前端类型检查
	cd web && $(NPM) run typecheck

.PHONY: build
build: web-build ## 构建二进制（含内嵌前端）
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/macpanel
	@echo "已生成 $(BINARY)（$(VERSION)）"

.PHONY: build-go
build-go: ## 只构建二进制（跳过前端，用于后端迭代）
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/macpanel

.PHONY: test
test: ## 运行全部 Go 测试
	$(GO) test ./... -count=1

.PHONY: test-verbose
test-verbose: ## 运行 Go 测试（详细输出）
	$(GO) test ./... -count=1 -v

.PHONY: test-race
test-race: ## 竞态检测（IPC 并发路径）
	$(GO) test ./... -count=1 -race

.PHONY: test-cover
test-cover: ## 覆盖率报告
	$(GO) test ./... -count=1 -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out | tail -20

.PHONY: vet
vet: ## go vet 静态检查
	$(GO) vet ./...

.PHONY: fmt
fmt: ## 格式化 Go 代码
	$(GO) fmt ./...

.PHONY: lint
lint: fmt vet ## 格式化 + 静态检查

.PHONY: check
check: lint test web-typecheck ## 提交前完整检查

.PHONY: dev-token
dev-token: ## 生成开发环境 IPC 令牌
	@mkdir -p .dev/run
	$(BINARY) token -dev -force

.PHONY: dev-agent
dev-agent: ## 前台启动 Agent（开发模式，非 root）
	$(BINARY) agent -dev

.PHONY: dev-server
dev-server: ## 前台启动 Server（开发模式）
	$(BINARY) server -dev

.PHONY: dev-status
dev-status: ## 用 CLI 查询 Agent 状态
	$(BINARY) status -dev

.PHONY: clean
clean: ## 清理构建产物
	rm -rf $(BIN_DIR) .dev/run .dev/logs coverage.out
	@echo "已清理（保留 .dev/data 中的数据）"

.PHONY: clean-all
clean-all: clean ## 清理全部产物（含数据与前端）
	rm -rf .dev web/dist web/node_modules

.PHONY: pkg
pkg: build ## 构建 PKG 安装包（需 root 权限验证安装）
	./packaging/macos/pkg/build-pkg.sh

.PHONY: verify-pkg
verify-pkg: ## 静态校验已构建的 PKG（不需 root，不会安装任何东西）
	./scripts/verify-pkg.sh

.PHONY: dmg
dmg: pkg ## 构建 DMG 分发容器
	./packaging/macos/dmg/build-dmg.sh
