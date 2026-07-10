module github.com/mow/mow/core

go 1.22

require github.com/mow/mow/sdk v0.0.0-00010101000000-000000000000

require gopkg.in/yaml.v3 v3.0.1 // indirect

// Workspace 模式下由 go.work 提供本地路径解析；
// 显式 replace 便于未来单独构建 core module（无 workspace 场景）。
replace github.com/mow/mow/sdk => ../sdk
