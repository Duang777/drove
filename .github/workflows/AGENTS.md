# AGENTS.md — .github/workflows

## 职责

**CI 流水线定义**（GitHub Actions）。

## 当前内容

- `ci.yml`：push/PR 触发；矩阵 Go 1.23 / 1.24；gofmt 校验 → go vet → go test -race（含覆盖率 artifact）→ 双二进制编译。

## 约束

- 修改流水线须保证：格式校验、静态检查、测试、构建四步齐全；新增步骤不得降低门禁强度。
- 工作流文件变更必须随代码 PR 一起评审。
