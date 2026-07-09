# Plugin SDK Proto

本目录存放 MOW Plugin 的 gRPC 接口定义（`.proto` 文件）。

- 由 `protoc` 生成 Go 代码到同目录（`*.pb.go` / `*_grpc.pb.go`）
- 语言无关：未来 Rust / Python / Node 插件也基于同一份 proto

首版 proto 将在下一步 RFC `sdk/proto/plugin.proto` 中提交。
