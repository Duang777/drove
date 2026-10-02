// Package version 保存编译期注入的版本信息。
package version

// 以下变量由 ldflags 注入：-X github.com/Duang777/drove/internal/version.Version=$(VERSION)
var (
	// Version 是语义化版本号；默认 dev。
	Version = "dev"
	// Commit 是 git 短提交；默认 unknown。
	Commit = "unknown"
	// Date 是构建时间（RFC3339）；默认 unknown。
	Date = "unknown"
)
