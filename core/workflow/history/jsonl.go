// jsonl.go 是 Store 的默认实现：append-only JSON Lines。
//
// 为什么用 JSONL：
//   - 无第三方依赖 / 零 CGO
//   - 增量写入 O(1)，crash-safe：单条写坏最多丢一行
//   - 用 `tail -f` / `jq` 就能人肉观测
//
// 局限：
//   - List 需要全文件扫描；到万级行会明显变慢 —— 到时用 SQLite 替换
//   - 没有事务：并发 Save 靠单进程 sync.Mutex + O_APPEND 保证一行一原子
//
// v0.3.1 增强（本文件）：
//   - RotateOptions：按字节大小做 append-only 轮转（`.jsonl.1` / `.jsonl.2` ...）
//   - MaxKeep：控制历史轮转文件保留个数，防止无限增长
//   - readAll 现在会跨主文件 + 轮转文件读取；List 依然按 FinishedAt 倒序
//   - 覆盖率：新增损坏行 / 超长行 / 部分行的抗回归测试（见 jsonl_test.go）

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

// -----------------------------------------------------------------------------
// RotateOptions
// -----------------------------------------------------------------------------

// RotateOptions 控制 JSONLStore 的滚动策略。
//
// 触发时机：每次 Save 写入前，Store 会用 os.Stat 检查当前主文件大小；
// 若 >= MaxBytes 就把主文件 rename 到 .jsonl.1（老的 .1 → .2，依此类推），
// 再新建空的主文件继续写。
//
// 设计上不做行数触发：JSONL 单行大小差异极大（含步骤数据 / 大 params），
// 用字节数是唯一 O(1) 判定，也和 log rotate 生态对齐。
type RotateOptions struct {
	// MaxBytes 主文件的字节上限；0 表示不轮转（等价于旧版行为）。
	MaxBytes int64

	// MaxKeep 保留最多多少个已轮转文件（`.jsonl.1` ~ `.jsonl.N`）。
	// 0 表示不限；负数被 clamp 到 0。
	// N+1 及以后的历史会在轮转时直接删除。
	MaxKeep int
}

// -----------------------------------------------------------------------------
// JSONLStore
// -----------------------------------------------------------------------------

// JSONLStore 把 Record 追加到 <dir>/workflow-runs.jsonl。
//
// 并发：
//   - 单进程内用 sync.Mutex 序列化 Save / rotate；List / Get 打开新 fd 读，无锁。
//   - 多进程共享同一文件时可能交叉写入；append-only + 单行 JSON 让"最坏丢一行"，
//     不会破坏其它行 —— 已足够 v0.3 的观测需求。真正的跨进程文件锁留给 v0.4+
//     引入 SQLite 时统一解决。
type JSONLStore struct {
	path   string
	mu     sync.Mutex
	rotate RotateOptions
}

// NewJSONLStore 创建 / 打开一个 JSONL 存储。dir 必须存在。
// 文件名固定为 workflow-runs.jsonl，与审计日志同目录共存。
// 不带轮转（等价旧行为）；需要轮转请用 NewJSONLStoreWithRotate。
func NewJSONLStore(dir string) (*JSONLStore, error) {
	return NewJSONLStoreWithRotate(dir, RotateOptions{})
}

// NewJSONLStoreWithRotate 与 NewJSONLStore 类似，另接受 RotateOptions。
// 传入零值 RotateOptions 时行为与旧版完全一致。
func NewJSONLStoreWithRotate(dir string, rot RotateOptions) (*JSONLStore, error) {
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
	if rot.MaxKeep < 0 {
		rot.MaxKeep = 0
	}
	return &JSONLStore{
		path:   filepath.Join(dir, "workflow-runs.jsonl"),
		rotate: rot,
	}, nil
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

	// 轮转（在同一把 mutex 下检查 + 执行，避免并发 Save 竞争 rename）
	if err := s.maybeRotate(int64(len(buf))); err != nil {
		return err
	}

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

// List 读取全文件（含已轮转文件）、按 FinishedAt 倒序返回。
//
// Limit=0 时使用默认 100；上限 500，防止 UI 无节制拉。
func (s *JSONLStore) List(_ context.Context, opts ListOptions) ([]Record, error) {
	all, err := s.readAllWithRotated()
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
	all, err := s.readAllWithRotated()
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

// -----------------------------------------------------------------------------
// 内部：读取所有文件（主文件 + 轮转历史）
// -----------------------------------------------------------------------------

// readAllWithRotated 读取主文件与所有已轮转文件，按主文件优先返回。
// 单个文件读取失败（除 ErrNotExist 外）会立即返回；单行 JSON 损坏则跳过。
func (s *JSONLStore) readAllWithRotated() ([]Record, error) {
	var out []Record
	// 主文件
	rows, err := readAllFromFile(s.path)
	if err != nil {
		return nil, err
	}
	out = append(out, rows...)
	// 轮转文件：`.1` `.2` ... 直到不存在
	for i := 1; ; i++ {
		p := fmt.Sprintf("%s.%d", s.path, i)
		rows, err := readAllFromFile(p)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			// 文件不存在 → 停止
			if _, statErr := os.Stat(p); errors.Is(statErr, os.ErrNotExist) {
				break
			}
		}
		out = append(out, rows...)
		// 防御：万一有极端多的历史文件，硬上限 1024 直接终止扫描
		if i >= 1024 {
			break
		}
	}
	return out, nil
}

// readAllFromFile 读单个 JSONL 文件；不存在时返回 (nil, nil)。
func readAllFromFile(path string) ([]Record, error) {
	f, err := os.Open(path)
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
		// 例如 bufio.ErrTooLong：说明有一条超出 4 MiB 的巨型行；
		// 已读到的记录仍返回，出错的位置以后再来（下次滚动会把它甩掉）。
		return out, fmt.Errorf("history: scan: %w", err)
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// 内部：轮转
// -----------------------------------------------------------------------------

// maybeRotate 若配置了 MaxBytes 且当前主文件 + 即将写入的字节数超阈值，
// 则做一次 rename 轮转：
//
//	workflow-runs.jsonl.(N-1) → .jsonl.N   (依次)
//	workflow-runs.jsonl        → .jsonl.1
//	（再新建空主文件继续追加）
//
// MaxKeep>0 时，超过保留个数的旧文件会被删除。
// 调用方必须持有 s.mu。
func (s *JSONLStore) maybeRotate(pendingBytes int64) error {
	if s.rotate.MaxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("history: stat rotate: %w", err)
	}
	if info.Size()+pendingBytes <= s.rotate.MaxBytes {
		return nil
	}
	return s.doRotate()
}

// doRotate 执行一次滚动。调用方必须持有 s.mu。
func (s *JSONLStore) doRotate() error {
	// 先删掉超出 MaxKeep 的最老文件。
	// 从当前实际最大编号开始向下删，避免每次都走 1024 次 stat/remove。
	if s.rotate.MaxKeep > 0 {
		top := highestRotated(s.path)
		for i := top; i > s.rotate.MaxKeep; i-- {
			p := fmt.Sprintf("%s.%d", s.path, i)
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("history: prune %s: %w", p, err)
			}
		}
	}
	// 再把 N-1 → N（倒序，避免覆盖）
	// 只需要 rename 到 MaxKeep 之内；MaxKeep=0 表示不限制
	upper := s.rotate.MaxKeep
	if upper <= 0 {
		// 无限保留：只 rename 现存的最大编号；扫一次已存在的
		upper = highestRotated(s.path) + 1
	}
	for i := upper; i >= 2; i-- {
		src := fmt.Sprintf("%s.%d", s.path, i-1)
		dst := fmt.Sprintf("%s.%d", s.path, i)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("history: rotate rename %s→%s: %w", src, dst, err)
		}
	}
	// 最后把主文件 → .1
	dst := s.path + ".1"
	if err := os.Rename(s.path, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("history: rotate main→.1: %w", err)
	}
	return nil
}

// highestRotated 返回目前最大的已轮转编号；无则返回 0。
func highestRotated(mainPath string) int {
	max := 0
	// 硬上限 1024，防止极端场景无限循环
	for i := 1; i <= 1024; i++ {
		p := fmt.Sprintf("%s.%d", mainPath, i)
		if _, err := os.Stat(p); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			// 其它错误也直接停，交给上层的写路径感知
			break
		}
		max = i
	}
	return max
}
