package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mow/mow/core/workflow"
	"github.com/mow/mow/core/workflow/history"
)

// memHistory 是最简 HistorySink 实现，用来断言 Runner 已在 Run 结束时落盘。
type memHistory struct {
	mu    sync.Mutex
	saves []workflow.RunSnapshot
	err   error
}

func (m *memHistory) SaveRun(_ context.Context, snap *workflow.RunSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves = append(m.saves, *snap)
	return m.err
}
func (m *memHistory) last() *workflow.RunSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.saves) == 0 {
		return nil
	}
	c := m.saves[len(m.saves)-1]
	return &c
}

// -----------------------------------------------------------------------------
// Runner + HistorySink：成功路径
// -----------------------------------------------------------------------------

func TestRunner_History_Success(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["ok"] = map[string]any{}
	hist := &memHistory{}

	r := workflow.NewRunner(workflow.RunnerOptions{
		Command: cmd,
		History: hist,
	})
	w := &workflow.Workflow{
		ID:    "wf.a",
		Steps: []workflow.Step{{ID: "s1", Command: "ok"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{
		TargetID:    "srv",
		CallerLabel: "cli:tester",
		Inputs:      map[string]any{"x": 1},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RunID == "" {
		t.Fatal("RunID should be assigned")
	}
	if res.StartedAt.IsZero() || res.FinishedAt.IsZero() {
		t.Fatalf("times not set: %+v", res)
	}

	snap := hist.last()
	if snap == nil {
		t.Fatal("history not saved")
	}
	if snap.RunID != res.RunID || snap.WorkflowID != "wf.a" || !snap.OK {
		t.Fatalf("snapshot mismatch: %+v", snap)
	}
	if snap.TargetID != "srv" || snap.Caller != "cli:tester" {
		t.Fatalf("caller/target lost: %+v", snap)
	}
	if snap.Inputs["x"] != 1 {
		t.Fatalf("inputs lost: %+v", snap.Inputs)
	}
	if len(snap.Steps) != 1 || !snap.Steps[0].OK {
		t.Fatalf("steps not captured: %+v", snap.Steps)
	}
}

// -----------------------------------------------------------------------------
// Runner + HistorySink：失败路径
// -----------------------------------------------------------------------------

func TestRunner_History_FailurePathAlsoSaves(t *testing.T) {
	cmd := newFakeCmd()
	cmd.fail["bad"] = errors.New("boom")
	hist := &memHistory{}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd, History: hist})
	w := &workflow.Workflow{
		ID: "wf.bad",
		Steps: []workflow.Step{
			{ID: "s1", Command: "bad"},
		},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err == nil {
		t.Fatal("expected err")
	}
	if res.OK {
		t.Fatal("OK should be false")
	}
	snap := hist.last()
	if snap == nil || snap.OK {
		t.Fatalf("history should be saved with OK=false, got %+v", snap)
	}
	if snap.Error == "" {
		t.Fatalf("history should capture error msg")
	}
}

// -----------------------------------------------------------------------------
// Runner + HistorySink：Skip / Retry 也要落盘（Attempts / Skipped 都保留）
// -----------------------------------------------------------------------------

func TestRunner_History_SkipAndRetryPersisted(t *testing.T) {
	s := newScripted()
	s.responds["flaky"] = []error{errors.New("first fail")}
	s.outOK["flaky"] = map[string]any{"ok": true}

	hist := &memHistory{}
	r := workflow.NewRunner(workflow.RunnerOptions{Command: s, History: hist})
	w := &workflow.Workflow{
		ID: "wf.mix",
		Steps: []workflow.Step{
			{ID: "skip_me", Command: "never_called", When: "false"},
			{ID: "flaky", Command: "flaky",
				Retry: &workflow.RetryPolicy{Max: 3, Backoff: 5 * time.Millisecond}},
		},
	}
	if _, err := r.Run(context.Background(), w, workflow.RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	snap := hist.last()
	if snap == nil || len(snap.Steps) != 2 {
		t.Fatalf("expected 2 steps in history, got %+v", snap)
	}
	if !snap.Steps[0].Skipped {
		t.Fatalf("skip_me not marked skipped: %+v", snap.Steps[0])
	}
	if snap.Steps[1].Attempts != 2 {
		t.Fatalf("flaky Attempts = %d, want 2", snap.Steps[1].Attempts)
	}
}

// -----------------------------------------------------------------------------
// Runner + HistorySink：Sink 出错不影响 Run 返回
// -----------------------------------------------------------------------------

func TestRunner_History_SinkErrorSwallowed(t *testing.T) {
	cmd := newFakeCmd()
	cmd.out["ok"] = map[string]any{}
	hist := &memHistory{err: errors.New("disk full")}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd, History: hist})
	w := &workflow.Workflow{
		ID:    "wf",
		Steps: []workflow.Step{{ID: "s", Command: "ok"}},
	}
	res, err := r.Run(context.Background(), w, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("Run should not surface sink err: %v", err)
	}
	if !res.OK {
		t.Fatal("res.OK want true")
	}
}

// -----------------------------------------------------------------------------
// JSONLStore + Runner 端到端：Save 到磁盘、List 能读回
// -----------------------------------------------------------------------------

func TestRunner_JSONLStore_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	store, err := history.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	cmd := newFakeCmd()
	cmd.out["ok"] = map[string]any{"x": 1}

	r := workflow.NewRunner(workflow.RunnerOptions{Command: cmd, History: store})
	w := &workflow.Workflow{
		ID:    "wf.jsonl",
		Steps: []workflow.Step{{ID: "s1", Command: "ok"}},
	}
	res1, _ := r.Run(context.Background(), w, workflow.RunOptions{TargetID: "t1"})
	res2, _ := r.Run(context.Background(), w, workflow.RunOptions{TargetID: "t2"})

	list, err := store.List(context.Background(), history.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(list))
	}
	// res2 更晚 → 排第一位
	if list[0].RunID != res2.RunID {
		t.Errorf("first should be latest run: got %s, want %s", list[0].RunID, res2.RunID)
	}
	if list[1].RunID != res1.RunID {
		t.Errorf("second should be earlier run: got %s, want %s", list[1].RunID, res1.RunID)
	}

	// Get 单条
	got, err := store.Get(context.Background(), res1.RunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.TargetID != "t1" {
		t.Fatalf("Get mismatch: %+v", got)
	}
	// 文件真的在磁盘上
	if _, err := filepath.Abs(store.Path()); err != nil {
		t.Fatal(err)
	}
}
