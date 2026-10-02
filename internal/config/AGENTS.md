# AGENTS.md — internal/config

## 职责

**配置加载与校验**。daemon 与 CLI 共享同一份配置结构，避免各组件各自读环境变量造成漂移。

## 关键设计

- `Config` 结构体是唯一配置模型；`Load(path)` 读取 TOML/JSON（MVP 用 JSON，经 `os.ReadFile` + `encoding/json`），缺失字段用默认值。
- `Defaults()` 提供安全默认值（数据目录、API 地址、事件缓冲大小）。
- `Validate()` 在启动早期校验（如数据目录可写、端口合法），失败即返回错误，避免运行到一半才炸。

## 约束

- 禁止在其它包硬编码路径/端口常量；一律从 Config 读取。
- 环境变量覆盖（如 `DROVE_DATA_DIR`）只允许在本包实现。
- 导出类型：`Config`。
