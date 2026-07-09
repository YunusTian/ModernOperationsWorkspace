# Plugin SDK Proto

MOW Plugin 与 Core 之间的 gRPC 接口定义。

- **协议命名空间**：`mow.plugin.v1`
- **Go 生成包**：`github.com/mow/mow/sdk/proto`（别名 `pluginpb`）
- **语言无关**：可用于 Go / Rust / Python / Node 等任意 gRPC 语言

## 版本演进规则

- **只追加，不修改**：新增字段编号必须往后追加，禁止改动已有字段编号 / 类型
- **破坏性变更**必须新起 `v2` package，并保留 `v1` 至少 2 个大版本
- CI 会通过 `buf breaking` 拦截破坏性变更

## 依赖工具

| 工具 | 说明 |
| --- | --- |
| [buf](https://buf.build) | Proto lint / breaking / 生成（推荐） |
| protoc + protoc-gen-go + protoc-gen-go-grpc | 备选方案 |

安装：

```powershell
# 方式 A（推荐）：buf
scoop install buf              # 或 winget install bufbuild.buf
buf --version

# 方式 B：protoc + Go 插件
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## 生成 Go 代码

在本目录执行：

```powershell
# 方式 A（推荐）
buf generate

# 方式 B（不使用 buf）
protoc `
  --go_out=. --go_opt=paths=source_relative `
  --go-grpc_out=. --go-grpc_opt=paths=source_relative `
  plugin.proto
```

生成物：

```
plugin.pb.go        # 消息类型
plugin_grpc.pb.go   # gRPC Server / Client
```

生成后请**不要手动改动**，改动始终发生在 `.proto` 文件。

## Lint 与 Breaking 检查

```powershell
buf lint
buf breaking --against '.git#branch=main,subdir=sdk/proto'
```

## 目录

```
sdk/proto/
├── plugin.proto     ← 主接口文件
├── buf.yaml         ← buf 配置（lint / breaking 规则）
├── buf.gen.yaml     ← buf 生成配置
└── README.md
```
