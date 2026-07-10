//go:build windows

package main

// npipe_windows.go —— Windows 平台上的 Named Pipe 拨号实现。
//
// v0.3.1 引入 github.com/Microsoft/go-winio。选它的原因：
//   - 官方 Docker Desktop 与 moby/moby 均使用该库
//   - 只涉及 DialPipe / DialPipeContext 少量表面，稳定性高
//   - 依赖体积极小，且仅在 windows build tag 下引入，不影响 Linux/macOS 交叉编译
//
// 通道命名一律以 `\\.\pipe\` 开头；resolveTarget 会把 `npipe:////./pipe/xxx`
// 归一化为 NetAddr=`//./pipe/xxx`，我们再补回 `\\.\pipe\xxx` 让 winio 接受。

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// npipeSupported 让上层无需重复做平台判定。
const npipeSupported = true

// dialNpipeContext 通过 go-winio 拨一次 Named Pipe。
// timeout=0 时使用 winio 的默认（无超时）；调用方通常靠 ctx 取消控制。
func dialNpipeContext(ctx context.Context, addr string) (net.Conn, error) {
	// 归一化：允许传入 `//./pipe/xxx` 或 `\\.\pipe\xxx`
	name := addr
	if strings.HasPrefix(name, "//") {
		// 把前导 // 转成 \\.
		name = `\\` + strings.TrimPrefix(name, "//")
		name = strings.ReplaceAll(name, "/", `\`)
	}
	// 有 ctx 就用 ctx；否则给一个合理超时避免永久 hang
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}
	return winio.DialPipeContext(ctx, name)
}
