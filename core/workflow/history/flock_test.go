// flock_test.go —— 验证 JSONLStore 的跨进程文件锁。
//
// 策略：
//   - 用同一测试二进制以特殊环境变量 MOW_HISTORY_FLOCK_WORKER=1 重新 exec 出
//     多个子进程；每个子进程各写入 N 条记录到共享目录里的同一 JSONL 文件。
//   - 主进程 wait 所有子进程完成后，读整份 JSONL：
//       * 总行数 == workers * N
//       * 每一行都能 json.Unmarshal 成一条合法 Record（无行内交错）
//
// 若 flock 失败降级为无锁（不太可能，本地测试都能拿到），单进程 mutex 也
// 不再起作用（毕竟是多个进程），此时可能出现行内交错——测试即会失败。
// 这一失败反过来提示环境不支持 advisory lock，需要在文档标注。
//
// 依赖：仅标准库 + 依赖 JSONLStore 自身；不需要引入额外 fixture。
package history_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow/history"
)

// 每个 worker 写入的记录数；总记录 = workers * writesPerWorker
const (
	flockWorkers         = 4
	flockWritesPerWorker = 50
)

// TestMain 让子进程分支尽早生效：MOW_HISTORY_FLOCK_WORKER=1 时不跑测试，
// 而是当 worker 用，写完就 os.Exit。
func TestMain(m *testing.M) {
	if os.Getenv("MOW_HISTORY_FLOCK_WORKER") == "1" {
		runFlockWorker()
		return
	}
	os.Exit(m.Run())
}

// runFlockWorker 是子进程入口：
//   - 环境变量 MOW_HISTORY_FLOCK_DIR：目标目录
//   - 环境变量 MOW_HISTORY_FLOCK_TAG：附加到 RunID 前缀，便于区分 worker
//   - 环境变量 MOW_HISTORY_FLOCK_N：写入条数
func runFlockWorker() {
	dir := os.Getenv("MOW_HISTORY_FLOCK_DIR")
	tag := os.Getenv("MOW_HISTORY_FLOCK_TAG")
	nStr := os.Getenv("MOW_HISTORY_FLOCK_N")
	n, err := strconv.Atoi(nStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: bad N=%q: %v\n", nStr, err)
		os.Exit(2)
	}
	s, err := history.NewJSONLStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker: NewJSONLStore: %v\n", err)
		os.Exit(2)
	}
	// 构造一个"故意较长"的 payload，让并发写更容易暴露行内交错问题。
	payload := map[string]any{
		"junk": func() string {
			b := make([]byte, 400)
			for i := range b {
				b[i] = 'x'
			}
			return string(b)
		}(),
	}
	for i := 0; i < n; i++ {
		rec := &history.Record{
			RunID:      fmt.Sprintf("%s-%d", tag, i),
			WorkflowID: "wf-flock",
			FinishedAt: time.Now().UTC(),
			OK:         true,
			Inputs:     payload,
		}
		if err := s.Save(context.Background(), rec); err != nil {
			fmt.Fprintf(os.Stderr, "worker %s save %d: %v\n", tag, i, err)
			os.Exit(3)
		}
	}
	os.Exit(0)
}

// TestJSONLStore_CrossProcessSaveNoInterleave 拉起 4 个子进程各写 50 条，
// 断言：总行数 = 200，每一行都能独立 json.Unmarshal。
func TestJSONLStore_CrossProcessSaveNoInterleave(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cross-process test in short mode")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()

	var wg sync.WaitGroup
	errCh := make(chan error, flockWorkers)
	for i := 0; i < flockWorkers; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			// 让子进程只跑 TestMain 的 worker 分支：随便传一个 run 目标即可。
			cmd := exec.Command(exe, "-test.run=^$")
			cmd.Env = append(os.Environ(),
				"MOW_HISTORY_FLOCK_WORKER=1",
				"MOW_HISTORY_FLOCK_DIR="+dir,
				"MOW_HISTORY_FLOCK_TAG=w"+strconv.Itoa(i),
				"MOW_HISTORY_FLOCK_N="+strconv.Itoa(flockWritesPerWorker),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errCh <- fmt.Errorf("worker %d failed: %v\n%s", i, err, out)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("cross-process worker: %v", err)
	}

	// 读回主文件，逐行校验。
	f, err := os.Open(filepath.Join(dir, "workflow-runs.jsonl"))
	if err != nil {
		t.Fatalf("open jsonl: %v", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	seen := 0
	for {
		var rec history.Record
		if err := dec.Decode(&rec); err != nil {
			// io.EOF 意味着扫完；其它错误意味着有行损坏 → 交叉写入
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode line %d (interleave?): %v", seen+1, err)
		}
		if rec.RunID == "" {
			t.Fatalf("empty run_id at line %d", seen+1)
		}
		seen++
	}
	want := flockWorkers * flockWritesPerWorker
	if seen != want {
		t.Fatalf("cross-process line count = %d, want %d", seen, want)
	}
}
