package history_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
	"github.com/mow/mow/core/workflow/history"
)

// -----------------------------------------------------------------------------
// JSONLStore：Save / List / Get / Filter / Limit
// -----------------------------------------------------------------------------

func newStore(t *testing.T) *history.JSONLStore {
	t.Helper()
	dir := t.TempDir()
	s, err := history.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	return s
}

func TestJSONLStore_SaveGet(t *testing.T) {
	s := newStore(t)
	rec := &history.Record{
		RunID:      "run-abc",
		WorkflowID: "wf.deploy",
		TargetID:   "srv-01",
		Caller:     "cli:alice",
		StartedAt:  time.Now().Add(-time.Second).UTC(),
		FinishedAt: time.Now().UTC(),
		Duration:   time.Second,
		OK:         true,
	}
	if err := s.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Get(context.Background(), "run-abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.RunID != "run-abc" || got.WorkflowID != "wf.deploy" {
		t.Fatalf("Get mismatch: %+v", got)
	}
}

func TestJSONLStore_ListOrderDescByFinishedAt(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	// 有意乱序 Save
	saves := []history.Record{
		{RunID: "a", WorkflowID: "wf", FinishedAt: base.Add(1 * time.Second), OK: true},
		{RunID: "b", WorkflowID: "wf", FinishedAt: base.Add(3 * time.Second), OK: true},
		{RunID: "c", WorkflowID: "wf", FinishedAt: base.Add(2 * time.Second), OK: true},
	}
	for i := range saves {
		if err := s.Save(context.Background(), &saves[i]); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d", len(list))
	}
	got := []string{list[0].RunID, list[1].RunID, list[2].RunID}
	want := []string{"b", "c", "a"} // desc by FinishedAt
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestJSONLStore_ListLimitAndFilter(t *testing.T) {
	s := newStore(t)
	base := time.Now().UTC()
	for i, wf := range []string{"a", "a", "b", "a", "b"} {
		_ = s.Save(context.Background(), &history.Record{
			RunID:      "r" + string(rune('0'+i)),
			WorkflowID: wf,
			FinishedAt: base.Add(time.Duration(i) * time.Second),
			OK:         true,
		})
	}
	// 按 workflow_id 过滤
	list, err := s.List(context.Background(), history.ListOptions{WorkflowID: "a"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("filter len = %d, want 3", len(list))
	}
	for _, r := range list {
		if r.WorkflowID != "a" {
			t.Fatalf("unexpected wf: %+v", r)
		}
	}
	// Limit
	list2, err := s.List(context.Background(), history.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list2) != 2 {
		t.Fatalf("limit len = %d, want 2", len(list2))
	}
	// 超上限
	list3, err := s.List(context.Background(), history.ListOptions{Limit: 9999})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list3) != 5 {
		t.Fatalf("cap len = %d, want 5", len(list3))
	}
}

func TestJSONLStore_GetMissingReturnsNilNil(t *testing.T) {
	s := newStore(t)
	got, err := s.Get(context.Background(), "nope")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestJSONLStore_ListEmptyFile(t *testing.T) {
	s := newStore(t)
	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("len = %d", len(list))
	}
}

func TestJSONLStore_CorruptLineSkipped(t *testing.T) {
	s := newStore(t)
	// 塞一条脏数据 + 一条正常数据
	f, err := os.OpenFile(s.Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString("this is not json\n")
	_, _ = f.WriteString(`{"run_id":"ok","workflow_id":"wf","ok":true,"finished_at":"2026-07-10T00:00:00Z"}` + "\n")
	_ = f.Close()

	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].RunID != "ok" {
		t.Fatalf("expected 1 clean row, got %+v", list)
	}
}

func TestNewJSONLStore_MissingDir(t *testing.T) {
	_, err := history.NewJSONLStore(filepath.Join(t.TempDir(), "no-such-child"))
	if err == nil {
		t.Fatal("expected error for missing dir")
	}
}

// -----------------------------------------------------------------------------
// SnapshotToRecord
// -----------------------------------------------------------------------------

func TestSnapshotToRecord(t *testing.T) {
	snap := &workflow.RunSnapshot{
		RunID: "r1", WorkflowID: "wf.deploy",
		TargetID: "srv", Caller: "cli:me",
		OK: false, Error: "boom",
		StartedAt:  time.Unix(100, 0).UTC(),
		FinishedAt: time.Unix(200, 0).UTC(),
		Duration:   100 * time.Second,
	}
	rec := history.SnapshotToRecord(snap)
	if rec.RunID != "r1" || rec.Error != "boom" || rec.Duration != 100*time.Second {
		t.Fatalf("bad record: %+v", rec)
	}
}

// -----------------------------------------------------------------------------
// noopStore.SaveRun 也能被 workflow.HistorySink 接口接住 —— 静态断言即可
// -----------------------------------------------------------------------------

var _ workflow.HistorySink = (*history.JSONLStore)(nil)

// 空接口断言：确保 Noop 也实现了 HistorySink（否则上层 App 无法直接用它作为兜底）。
func TestNoopImplementsSink(t *testing.T) {
	var sink any = history.Noop()
	if _, ok := sink.(workflow.HistorySink); !ok {
		t.Fatal("Noop() must implement workflow.HistorySink")
	}
	if _, ok := sink.(history.Store); !ok {
		t.Fatal("Noop() must implement history.Store")
	}
}

// 抗回归：Noop().SaveRun 不应报错
func TestNoop_SaveRunOK(t *testing.T) {
	sink := history.Noop().(workflow.HistorySink)
	if err := sink.SaveRun(context.Background(), &workflow.RunSnapshot{RunID: "x"}); err != nil {
		t.Fatalf("noop SaveRun err = %v", err)
	}
}

// 抗回归：Save 拒绝空 run_id
func TestJSONLStore_SaveRequiresRunID(t *testing.T) {
	s := newStore(t)
	err := s.Save(context.Background(), &history.Record{WorkflowID: "wf"})
	if err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("expected run_id required error, got %v", err)
	}
}

// 抗回归：Save nil 明确报错
func TestJSONLStore_SaveNil(t *testing.T) {
	s := newStore(t)
	if err := s.Save(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil record")
	} else if !errors.Is(err, err) { // 简单 sanity
		// no-op
	}
}

// -----------------------------------------------------------------------------
// v0.3.1 —— 轮转 / 并发 / 损坏行恢复
// -----------------------------------------------------------------------------

// TestJSONLStore_RotateAndReadAcrossFiles 验证：
//   - 达到 MaxBytes 时，主文件被 rename 到 .1，新纪录写入新的主文件
//   - List 能跨主文件 + 轮转文件读到所有记录，且按 FinishedAt 倒序
func TestJSONLStore_RotateAndReadAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	// 单条记录约 200~250 字节；把 MaxBytes 设为 300 让每 2 条就触发一次轮转
	s, err := history.NewJSONLStoreWithRotate(dir, history.RotateOptions{
		MaxBytes: 300,
		MaxKeep:  5,
	})
	if err != nil {
		t.Fatalf("NewJSONLStoreWithRotate: %v", err)
	}
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		rec := &history.Record{
			RunID:      "r" + string(rune('0'+i)),
			WorkflowID: "wf",
			FinishedAt: base.Add(time.Duration(i) * time.Second),
			OK:         true,
		}
		if err := s.Save(context.Background(), rec); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	// 检查确实产生了轮转文件
	if _, err := os.Stat(filepath.Join(dir, "workflow-runs.jsonl.1")); err != nil {
		t.Fatalf("expected rotated .1 to exist: %v", err)
	}

	// List 应能跨文件读到全部 6 条，且顺序倒序
	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 6 {
		t.Fatalf("expected 6 records after rotate, got %d", len(list))
	}
	if list[0].RunID != "r5" || list[5].RunID != "r0" {
		t.Fatalf("order wrong: first=%s last=%s", list[0].RunID, list[5].RunID)
	}
}

// TestJSONLStore_RotateMaxKeepPrunesOldest 验证 MaxKeep=1 时最老文件被删。
func TestJSONLStore_RotateMaxKeepPrunesOldest(t *testing.T) {
	dir := t.TempDir()
	s, err := history.NewJSONLStoreWithRotate(dir, history.RotateOptions{
		MaxBytes: 200, // 让每次写入几乎都触发轮转
		MaxKeep:  1,
	})
	if err != nil {
		t.Fatalf("NewJSONLStoreWithRotate: %v", err)
	}
	for i := 0; i < 4; i++ {
		_ = s.Save(context.Background(), &history.Record{
			RunID:      "r" + string(rune('0'+i)),
			WorkflowID: "wf",
			FinishedAt: time.Now().Add(time.Duration(i) * time.Second),
			OK:         true,
		})
	}
	// 只应保留 .1；.2 及以后被 prune
	if _, err := os.Stat(filepath.Join(dir, "workflow-runs.jsonl.1")); err != nil {
		t.Fatalf(".1 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "workflow-runs.jsonl.2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".2 should be pruned, got err=%v", err)
	}
}

// TestJSONLStore_ConcurrentSaveNoInterleave 验证并发 Save：
//   - 100 个 goroutine 各 Save 一次；结果应 100% 落盘、无行内交错
//   - 每一行都能被 json.Unmarshal 独立解析
func TestJSONLStore_ConcurrentSaveNoInterleave(t *testing.T) {
	s := newStore(t)
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			err := s.Save(context.Background(), &history.Record{
				RunID:      "run-" + strconv.Itoa(i),
				WorkflowID: "wf",
				FinishedAt: time.Now(),
				OK:         true,
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent save: %v", err)
	}

	list, err := s.List(context.Background(), history.ListOptions{Limit: 500})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != n {
		t.Fatalf("expected %d records, got %d (some lines corrupted?)", n, len(list))
	}
}

// TestJSONLStore_CorruptLineMixedWithRotatedFile 验证：
// 主文件里插一条脏行，另一部分记录已经因为轮转跑到 .1 —— 都要能读出来。
func TestJSONLStore_CorruptLineMixedWithRotatedFile(t *testing.T) {
	dir := t.TempDir()
	s, err := history.NewJSONLStoreWithRotate(dir, history.RotateOptions{
		MaxBytes: 200,
		MaxKeep:  3,
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	// 先塞 3 条：会触发轮转，把前面部分放到 .1
	for i := 0; i < 3; i++ {
		_ = s.Save(context.Background(), &history.Record{
			RunID:      "r" + string(rune('0'+i)),
			WorkflowID: "wf",
			FinishedAt: time.Now().Add(time.Duration(i) * time.Second),
			OK:         true,
		})
	}
	// 直接在主文件里追加一条脏行
	main := s.Path()
	f, err := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open main: %v", err)
	}
	_, _ = f.WriteString("!!! this is not json !!!\n")
	_ = f.Close()

	// List 应跳过脏行，正常记录数 == 3
	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 clean records across main+rotated, got %d", len(list))
	}
}

// TestJSONLStore_ReadEmptyLinesTolerated 验证空行不被计入结果。
func TestJSONLStore_ReadEmptyLinesTolerated(t *testing.T) {
	s := newStore(t)
	f, err := os.OpenFile(s.Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = f.WriteString("\n\n\n")
	_, _ = f.WriteString(`{"run_id":"ok","workflow_id":"wf","ok":true,"finished_at":"2026-07-10T00:00:00Z"}` + "\n")
	_, _ = f.WriteString("\n")
	_ = f.Close()

	list, err := s.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 clean row, got %d", len(list))
	}
}

// TestJSONLStore_RotateNoOpWhenDisabled 验证 MaxBytes<=0 时永不轮转。
func TestJSONLStore_RotateNoOpWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	s, err := history.NewJSONLStoreWithRotate(dir, history.RotateOptions{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// 写足够多的数据（超过任何合理的默认 MaxBytes）；不应产生 .1
	for i := 0; i < 50; i++ {
		_ = s.Save(context.Background(), &history.Record{
			RunID:      "r" + strconv.Itoa(i),
			WorkflowID: "wf",
			FinishedAt: time.Now(),
			OK:         true,
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "workflow-runs.jsonl.1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".1 should NOT exist when rotation is disabled, err=%v", err)
	}
}

// TestNewJSONLStoreWithRotate_NegativeMaxKeepClamped 验证负值被 clamp。
func TestNewJSONLStoreWithRotate_NegativeMaxKeepClamped(t *testing.T) {
	dir := t.TempDir()
	_, err := history.NewJSONLStoreWithRotate(dir, history.RotateOptions{MaxKeep: -3})
	if err != nil {
		t.Fatalf("should tolerate negative MaxKeep: %v", err)
	}
}
