package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeBuilder 实现 packageBuilder：把一段可变字节写到 binOut。
// 每轮 Build 前测试代码可以 mutate Content 或调整 seq 让 checksum 变化，
// 从而模拟真实 `go build` 产出不同的二进制。
type fakeBuilder struct {
	Content []byte
	Calls   int
}

func (f *fakeBuilder) Build(_, binOut, _, _ string) error {
	f.Calls++
	if err := os.MkdirAll(filepath.Dir(binOut), 0o755); err != nil {
		return err
	}
	content := f.Content
	if content == nil {
		content = []byte("fake-binary-" + binOut)
	}
	return os.WriteFile(binOut, content, 0o755)
}

// initPluginSource 通过 `mow plugin init` 生成一份最小骨架，返回源码根。
// 复用现有 CLI 命令，避免测试重复描述 plugin.json 全字段。
func initPluginSource(t *testing.T, id string) string {
	t.Helper()
	tmp := t.TempDir()
	target := filepath.Join(tmp, id)
	if _, _, err := runPluginDevCLI(t, "plugin", "init", id, "--dir", target); err != nil {
		t.Fatalf("scaffold %s: %v", id, err)
	}
	return target
}

// TestPluginDev_FirstRunInstallsAndEnables 覆盖首次执行：
// staging → Lifecycle.Install → SetEnabled(true)。
func TestPluginDev_FirstRunInstallsAndEnables(t *testing.T) {
	srcDir := initPluginSource(t, "devfoo")
	pluginsDir := t.TempDir()

	var out, errBuf bytes.Buffer
	fb := &fakeBuilder{Content: []byte("v1")}
	err := runPluginDev(context.Background(), &out, &errBuf, nil, pluginDevOpts{
		Dir:        srcDir,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Builder:    fb,
		PluginsDir: pluginsDir,
	})
	if err != nil {
		t.Fatalf("plugin dev: %v\nstderr:\n%s", err, errBuf.String())
	}
	if fb.Calls != 1 {
		t.Errorf("builder should be called once, got %d", fb.Calls)
	}

	// 装配到 <PluginsDir>/devfoo：plugin.json 与 bin/entrypoint 均存在。
	pluginRoot := filepath.Join(pluginsDir, "devfoo")
	if _, err := os.Stat(filepath.Join(pluginRoot, "plugin.json")); err != nil {
		t.Errorf("plugin.json missing after install: %v", err)
	}
	// state 里 enabled=true
	stateBytes, err := os.ReadFile(filepath.Join(pluginsDir, ".state", "devfoo.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !bytes.Contains(stateBytes, []byte(`"enabled": true`)) {
		t.Errorf("dev install should auto-enable: %s", stateBytes)
	}
	if !bytes.Contains(out.Bytes(), []byte("installed devfoo@0.1.0 (enabled).")) {
		t.Errorf("stdout missing install message:\n%s", out.String())
	}
}

// TestPluginDev_SecondRunUpdates 覆盖已安装 → Update 分支。
func TestPluginDev_SecondRunUpdates(t *testing.T) {
	srcDir := initPluginSource(t, "devbar")
	pluginsDir := t.TempDir()

	fb := &fakeBuilder{Content: []byte("v1")}
	opts := pluginDevOpts{
		Dir:        srcDir,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Builder:    fb,
		PluginsDir: pluginsDir,
	}
	var out1 bytes.Buffer
	if err := runPluginDev(context.Background(), &out1, &out1, nil, opts); err != nil {
		t.Fatalf("first dev: %v", err)
	}

	// 二次运行前改写 fake 二进制内容，让 checksum 变化，Update 路径必须走通。
	fb.Content = []byte("v2-different")
	var out2 bytes.Buffer
	if err := runPluginDev(context.Background(), &out2, &out2, nil, opts); err != nil {
		t.Fatalf("second dev: %v\nstdout:\n%s", err, out2.String())
	}
	if fb.Calls != 2 {
		t.Errorf("builder should be called twice, got %d", fb.Calls)
	}
	if !bytes.Contains(out2.Bytes(), []byte("updated devbar@0.1.0 (enabled).")) {
		t.Errorf("second run should report update, got:\n%s", out2.String())
	}
}

// TestPluginDev_WatchTriggersRebuildOnMtimeBump 用 --watch + MaxCycles=1
// 精确断言：source mtime 前进 → devCycle 再次触发。
func TestPluginDev_WatchTriggersRebuildOnMtimeBump(t *testing.T) {
	srcDir := initPluginSource(t, "devwatch")
	pluginsDir := t.TempDir()

	fb := &fakeBuilder{Content: []byte("v1")}
	opts := pluginDevOpts{
		Dir:        srcDir,
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Watch:      true,
		Interval:   20 * time.Millisecond,
		Builder:    fb,
		PluginsDir: pluginsDir,
		MaxCycles:  1, // 观察到第一次「watch-driven cycle」即退出
	}

	// 在后台起 watch loop；期间把 main.go 的 mtime 向未来推进，触发变更检测。
	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- runPluginDev(context.Background(), &buf, &buf, nil, opts)
	}()

	// 给 first cycle 一点时间落地；然后 bump main.go 的 mtime。
	time.Sleep(80 * time.Millisecond)
	mainGo := filepath.Join(srcDir, "main.go")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(mainGo, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch loop failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch loop did not exit within 3s after mtime bump")
	}

	// 至少 2 次 build：first + watch-driven。
	if fb.Calls < 2 {
		t.Errorf("expected >=2 builds (first + watched), got %d", fb.Calls)
	}
}

// TestPluginDev_RejectsCrossPlatform 覆盖 host 平台强绑定校验。
func TestPluginDev_RejectsCrossPlatform(t *testing.T) {
	srcDir := initPluginSource(t, "devplat")
	pluginsDir := t.TempDir()

	otherOS := "linux"
	if runtime.GOOS == "linux" {
		otherOS = "darwin"
	}
	var out bytes.Buffer
	err := runPluginDev(context.Background(), &out, &out, nil, pluginDevOpts{
		Dir:        srcDir,
		GOOS:       otherOS,
		GOARCH:     runtime.GOARCH,
		Builder:    &fakeBuilder{},
		PluginsDir: pluginsDir,
	})
	if err == nil {
		t.Fatal("cross-platform dev should fail")
	}
}
