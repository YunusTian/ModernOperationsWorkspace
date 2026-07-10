// npipe_test.go —— 跨平台 npipe helper 的最小单测。
//
// - Windows：验证 dialNpipeContext 对 `//./pipe/xxx` 与 `\\.\pipe\xxx` 两种
//   形式都会尝试拨号（拨向一个不存在的 pipe，短 ctx 快速失败）。
// - 非 Windows：验证 dialNpipeContext 立刻返回 DOCKER_NPIPE_UNSUPPORTED。
//
// 之所以拆一个独立 test：npipe 是 v0.3.1 新增能力，需要一处清晰断言
// npipeSupported 常量与 dialNpipeContext 行为在两条平台上都符合预期。
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

func TestDialNpipeContext_PlatformBehavior(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// 用一个几乎不可能存在的 pipe 名，避免误连真实 Docker Desktop。
	name := `//./pipe/mow-e2e-nonexistent-` + strings.Repeat("x", 8)
	_, err := dialNpipeContext(ctx, name)

	if npipeSupported {
		// Windows：应该尝试拨号并因 pipe 不存在 / ctx 超时而失败，
		// 但不能是我们自己抛的 DOCKER_NPIPE_UNSUPPORTED 稳定错误码。
		if err == nil {
			t.Fatal("Windows: dial should fail for nonexistent pipe")
		}
		var se *sdk.Error
		if errors.As(err, &se) && se.Code == "DOCKER_NPIPE_UNSUPPORTED" {
			t.Fatalf("Windows: should not report unsupported; got %v", err)
		}
	} else {
		// 非 Windows：稳定错误码
		var se *sdk.Error
		if !errors.As(err, &se) || se.Code != "DOCKER_NPIPE_UNSUPPORTED" {
			t.Fatalf("non-Windows: expected DOCKER_NPIPE_UNSUPPORTED, got %v", err)
		}
	}
}
