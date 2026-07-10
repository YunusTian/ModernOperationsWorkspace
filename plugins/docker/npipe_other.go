//go:build !windows

package main

// npipe_other.go —— 非 Windows 平台上的 Named Pipe 存根。
//
// Windows Named Pipe 只在 Windows 上有意义；其它平台调用即视为编程错误，
// 返回稳定 sdk.Error 让上层清晰传递。
//
// 之所以拆平台文件而不是运行时 GOOS 判断：
//   - 避免把 github.com/Microsoft/go-winio 依赖引到 Linux/macOS 构建
//   - CGO_ENABLED=0 + 交叉编译时依然干净

import (
	"context"
	"net"

	"github.com/mow/mow/sdk"
)

// npipeSupported 让上层无需重复做平台判定。
const npipeSupported = false

// dialNpipeContext 在非 Windows 平台上永远失败。
func dialNpipeContext(_ context.Context, _ string) (net.Conn, error) {
	return nil, sdk.NewError("DOCKER_NPIPE_UNSUPPORTED",
		"npipe transport is only available on Windows", nil)
}
