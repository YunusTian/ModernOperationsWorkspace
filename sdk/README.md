# MOW Plugin SDK

给插件开发者的 Go SDK。

- 目标：**让写插件像写一个 Go struct 一样简单**
- 不需要了解 gRPC / hashicorp/go-plugin 细节，SDK 内部会桥接

## 最小示例

```go
package main

import (
    "context"
    "encoding/json"

    "github.com/mow/mow/sdk"
    "github.com/mow/mow/sdk/pluginserve"
)

// ---- Plugin ----

type SSHPlugin struct{}

func (p *SSHPlugin) Metadata() sdk.Metadata {
    return sdk.Metadata{
        ID:          "ssh",
        Name:        "SSH",
        Version:     "0.1.0",
        Author:      "mow",
        Description: "SSH connection & command execution",
        CoreVersion: ">=0.1.0,<0.2.0",
        ConnectionTypes: []string{"ssh"},
    }
}

func (p *SSHPlugin) Init(ctx context.Context, req sdk.InitRequest) error {
    // 解码用户配置
    return nil
}

func (p *SSHPlugin) Shutdown(ctx context.Context) error { return nil }

func (p *SSHPlugin) HealthCheck(ctx context.Context) sdk.HealthStatus {
    return sdk.StatusHealthy
}

func (p *SSHPlugin) Commands() []sdk.CommandHandler {
    return []sdk.CommandHandler{&ExecHandler{}}
}

// ---- Command: ssh.exec ----

type ExecHandler struct{}

func (h *ExecHandler) Spec() sdk.CommandSpec {
    return sdk.CommandSpec{
        ID:             "exec",
        Description:    "在目标主机执行 Shell 命令",
        Permission:     sdk.PermExecute,
        Streaming:      false,
        ConnectionType: "ssh",
    }
}

type execParams struct {
    Cmd string `json:"cmd"`
}

func (h *ExecHandler) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
    var p execParams
    if err := json.Unmarshal(req.Params, &p); err != nil {
        return nil, sdk.ErrParamInvalid
    }
    if req.Connection == nil {
        return nil, sdk.ErrConnectionRequired
    }
    // 真正的 SSH 执行……
    data, _ := json.Marshal(map[string]string{"stdout": "hello"})
    return &sdk.ExecuteResponse{Data: data}, nil
}

func (h *ExecHandler) ExecuteStream(ctx context.Context, s sdk.Stream) error {
    return sdk.ErrNotSupported
}

// ---- 进程入口 ----

func main() {
    pluginserve.Serve(&SSHPlugin{})
}
```

## SDK 分层

| 目录 | 职责 |
| --- | --- |
| `sdk/` | Plugin / Command / Stream / Error 等对外抽象 |
| `sdk/pluginserve/` | 插件进程入口：`pluginserve.Serve(...)` |
| `sdk/pluginclient/` | Core 侧加载器：`pluginclient.LoadFromBinary(...)` |
| `sdk/proto/` | gRPC 接口定义 + 生成物 |
| `sdk/internal/grpcbridge/` | proto ↔ Go 类型桥接（不对外） |

## 核心概念

### Plugin

一个进程 = 一个 Plugin。插件在 `Metadata()` 中声明自己提供哪些 Command。

### Command 两种形态

- **一次性**：实现 `Execute`，返回结构化数据
- **流式**：`Spec().Streaming = true`，实现 `ExecuteStream`，用 [Stream](#stream) 收发数据

**不得同时实现两者**——未使用的方法请返回 `sdk.ErrNotSupported`。

### Stream

流式 Command 的核心抽象。既能推送 `Stdout / Stderr / Event`，也能通过 `Recv()` 读取用户 `Stdin` 与 `Signal`（例如取消、终端 resize）。

### Permission

所有 Command 必须声明权限（`PermRead` / `PermWrite` / `PermExecute` / `PermDangerous`）。

- **Dangerous**：Core 会强制二次确认，插件必须在 `req.Confirmed = false` 时返回 `ErrConfirmationRequired`。

### 错误处理

统一使用 `sdk.Error`，支持 `errors.Is`：

```go
return nil, sdk.NewError("SSH_AUTH_FAILED", "authentication failed", err).
    WithRetryable(false).
    WithDetails(map[string]any{"host": host})
```

## 单元测试建议

```go
func TestPluginValidate(t *testing.T) {
    if err := sdk.Validate(&SSHPlugin{}); err != nil {
        t.Fatal(err)
    }
}
```

## 未来演进

- `sdk/internal/grpcbridge`：`Serve` 内部接入 hashicorp/go-plugin
- `sdk/testkit`：本地内存桥，用于插件的表驱动测试
- `sdk/schema`：帮助生成 JSON Schema 的辅助函数
