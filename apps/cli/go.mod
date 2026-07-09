module github.com/mow/mow/apps/cli

go 1.22

require (
	github.com/mow/mow/core v0.0.0-00010101000000-000000000000
	github.com/mow/mow/sdk v0.0.0-00010101000000-000000000000
)

// Workspace 模式下由 go.work 提供本地路径解析；
// 显式 replace 便于未来单独构建时明确依赖来源。
replace (
	github.com/mow/mow/core => ../../core
	github.com/mow/mow/sdk => ../../sdk
)
