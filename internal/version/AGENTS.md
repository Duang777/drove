# AGENTS.md — internal/version

## 职责

编译期版本信息。值由 ldflags 注入（见 Makefile / CI），禁止硬编码版本号于源码。

## 约束

- `Version` / `Commit` / `Date` 默认值为 "dev"，仅当构建时传入 ldflags 才会被替换。
- 其它包只读本包变量，禁止自行定义版本常量。
