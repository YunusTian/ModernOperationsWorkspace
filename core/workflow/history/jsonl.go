// jsonl.go 是 Store 的默认实现：append-only JSON Lines。
//
// 为什么用 JSONL：
//   - 无第三方依赖 / 零 CGO
//   - 增量写入 O(1)，crash-safe：单条写坏最多丢一行
//   - 用 `tail -f` / `jq` 就能人肉观测
//
// 局限：
//   - List 需要全文件扫描；到万级行会明显变慢 —— 到时用 SQLite 替换
//   - 没有事务：并发 Save 靠文件锁 + O_APPEND 保证一行一原子

package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/mow/mow/core/workflow"
)

// JSONLStore 把 Record 追加到 <dir>/workflow-runs.jsonl。
//
// 并发：
//   - 单进程内用 sync.Mutex 序列化 Save；List / Get 打开新 fd 读，无锁。
//   - 多进程共享同一文件时可能交叉写入；append-only + 单行 JSON 让"最坏丢一行"，
//     不会破坏其它行 —— 已足够 v0.3 的观测需求。
type JSONLStore struct {
	path string
	mu   sync.Mutex
}

// NewJSONLStore 创建 / 打开一个 JSONL 存储。dir 必须存在。
// 文件名固定为 workflow-runs.jsonl，与审计日志同目录共存。
func NewJSONLStore(dir string) (*JSONLStore, error) {
	if dir == "" {
		return nil, errors.New("history: data dir is empty")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("history: stat data_dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("history: %q is not a directory", dir)
	}
	return &JSONLStore{path: filepath.Join(dir, "workflow-runs.jsonl")}, nil
}

// Path 返回底层文件路径（测试与运维定位用）。
func (s *JSONLStore) Path() string { return s.path }

// Save 追加一条记录。
func (s *JSONLStore) Save(_ context.Context, rec *Record) error {
	if rec == nil {
		return errors.New("history: record is nil")
	}
	if rec.RunID == "" {
		return errors.New("history: run_id is required")
	}
	buf, err := encodeRecord(rec)
	if err != nil {
		return fmt.Errorf("history: encode: %w", err)
	}
	buf = append(buf, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("history: write: %w", err)
	}
	return nil
}

// SaveRun 满足 workflow.HistorySink：把 snapshot 转 Record 再 Save。
func (s *JSONLStore) SaveRun(ctx context.Context, snap *workflow.RunSnapshot) error {
	return s.Save(ctx, SnapshotToRecord(snap))
}

// List 读取全文件、按 FinishedAt 倒序返回。
//
// Limit=0 时使用默认 100；上限 500，防止 UI 无节制拉。
func (s *JSONLStore) List(_ context.Context, opts ListOptions) ([]Record, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	// 过滤
	if opts.WorkflowID != "" {
		filtered := all[:0]
		for _, r := range all {
			if r.WorkflowID == opts.WorkflowID {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}
	// 倒序：新在前。JSONL 是追加写入，末尾一般是最新，但为了保险按 FinishedAt 排序。
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].FinishedAt.After(all[j].FinishedAt)
	})
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// Get 查找单条记录。找不到返回 (nil, nil)。
func (s *JSONLStore) Get(_ context.Context, runID string) (*Record, error) {
	if runID == "" {
		return nil, errors.New("history: run_id is empty")
	}
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	// 从后往前扫，最新的同 ID 优先（对齐 Save 允许追加的语义）。
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].RunID == runID {
			r := all[i]
			return &r, nil
		}
	}
	return nil, nil
}

func (s *JSONLStore) readAll() ([]Record, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()

	var out []Record
	scanner := bufio.NewScanner(f)
	// 单行 JSON 可能包含较大的 params/steps；把上限拉到 4 MiB。
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			// 单行坏了继续读下一行，避免一坨脏数据卡住整个历史
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("history: scan: %w", err)
	}
	return out, nil
}
