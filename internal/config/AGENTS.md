# AGENTS.md — internal/config

## 职责

**配置加载与校验**。daemon 与 CLI 共享同一份配置结构，避免各组件各自读环境变量造成漂移。

## 关键设计

- `Config` 结构体是唯一配置模型；`Load(path)` 读取 JSON，空路径解析为 `~/.drove/config.json`，缺失文件或字段使用默认值。
- `LoadResolved(path)` 同时返回绝对配置路径，供 CLI 自动拉起 daemon 时精确透传。
- `Defaults()` 提供安全默认值（数据目录、loopback API 地址、事件缓冲大小、本地控制台 Origin）。
- `Validate()` 在启动早期校验；API 只允许 loopback 监听，WebSocket Origin 只允许配置的本地 HTTP Origin。

## 约束

- 禁止在其它包硬编码路径/端口常量；一律从 Config 读取。
- 环境变量覆盖（如 `DROVE_DATA_DIR`）只允许在本包实现。
- 导出类型：`Config`。
