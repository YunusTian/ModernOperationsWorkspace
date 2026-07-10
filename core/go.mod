module github.com/mow/mow/core

go 1.25.0

require (
	github.com/expr-lang/expr v1.17.8
	github.com/hashicorp/go-hclog v0.14.1
	github.com/mow/mow/sdk v0.0.0-00010101000000-000000000000
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/fatih/color v1.7.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-plugin v1.6.2 // indirect
	github.com/hashicorp/yamux v0.1.1 // indirect
	github.com/mattn/go-colorable v0.1.4 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/oklog/run v1.0.0 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/grpc v1.68.1 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

// Workspace 模式下由 go.work 提供本地路径解析；
// 显式 replace 便于未来单独构建 core module（无 workspace 场景）。
replace github.com/mow/mow/sdk => ../sdk
