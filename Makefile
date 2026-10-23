.PHONY: help build run test test-race vet fmt check docs-check cover cover-html tidy lint hooks clean

# 默认目标：显示帮助
.DEFAULT_GOAL := help

BINARY := bin/agent2api
PKG    := ./cmd/agent2api
PACKAGES := ./cmd/... ./internal/...

help: ## 显示可用目标
	@echo "Agent2API 可用目标："
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## 编译到 bin/agent2api
	# -trimpath：去掉二进制中的绝对构建路径（避免泄漏本机目录结构，同时可复现构建）
	go build -trimpath -o $(BINARY) $(PKG)

run: build ## 编译并运行
	./$(BINARY)

test: ## 运行全部测试
	go test $(PACKAGES)

test-race: ## 运行测试并做竞态检测（CI 也跑这个）
	go test $(PACKAGES) -race

vet: ## 静态检查
	go vet $(PACKAGES)

fmt: ## 格式化
	gofmt -w cmd internal

check: fmt vet test docs-check ## 提交前自查：格式化 + 静态检查 + 测试 + 文档

docs-check: ## 离线检查文档路径、标题锚点和代码块（需要 Python 3.9+）
	python3 scripts/check-docs.py

cover: ## 显示覆盖率（按包）
	go test $(PACKAGES) -cover

cover-html: ## 生成 HTML 覆盖率报告
	go test $(PACKAGES) -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "已生成 coverage.html"

tidy: ## 整理依赖
	go mod tidy

lint: ## 运行 golangci-lint（未安装时给出提示）
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run $(PACKAGES); \
	else \
		echo "未安装 golangci-lint，跳过。安装：brew install golangci-lint"; \
	fi

hooks: ## 安装 git pre-commit 钩子（阻止凭证被提交）
	cp scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "✅ 已安装 .git/hooks/pre-commit —— 提交时会自动扫描凭证特征串"

clean: ## 清理编译产物与覆盖率报告
	rm -f $(BINARY) $(BINARY).exe coverage.out coverage.html
