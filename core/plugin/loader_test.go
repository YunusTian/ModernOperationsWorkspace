package plugin_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// TestSubprocessPluginEndToEnd 编译 plugins/ssh 为可执行文件，
// 通过 hashicorp/go-plugin 拉起它，注册进 Manager 后调用一次 ssh.ping。
//
// 目的是验证 sdk/internal/grpcbridge + pluginclient + PluginManager 的端到端链路。
// 该测试对网络无依赖，但需要工作区能编译（本机 go 可用）。
func TestSubprocessPluginEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skip subprocess test in -short mode")
	}

	// 1. 编译插件二进制到临时目录
	tmp := t.TempDir()
	binName := "mow-plugin-ssh"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	binPath := filepath.Join(tmp, binName)

	// 找 plugins/ssh 源码目录（从当前测试文件向上定位仓库根）
	repoRoot := findRepoRoot(t)
	src := filepath.Join(repoRoot, "plugins", "ssh")

	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build plugin: %v\n%s", err, out)
	}

	// 2. 加载插件
	lp, err := plugin.LoadFromBinary(binPath, nil)
	if err != nil {
		t.Fatalf("LoadFromBinary: %v", err)
	}
	defer lp.Close()

	// 3. 注册并启用
	mgr := plugin.NewManager(plugin.Options{})
	if err := mgr.Register(lp.Plugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Enable(ctx, "ssh", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// 4. 找到 ping 命令并调用
	h, err := mgr.Command("ssh", "ping")
	if err != nil {
		t.Fatalf("Command lookup: %v", err)
	}

	resp, err := h.Execute(ctx, &sdk.ExecuteRequest{AuditID: "test-1"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("decode: %v (data=%s)", err, string(resp.Data))
	}
	if got["pong"] != "ok" {
		t.Errorf("unexpected response: %+v", got)
	}
}

// findRepoRoot 从当前工作目录向上寻找 go.work，作为仓库根。
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot locate repo root (go.work not found)")
		}
		dir = parent
	}
}
