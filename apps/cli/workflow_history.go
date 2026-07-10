// workflow_history.go 实现 `mow workflow history [list|show]` 子命令。
//
// 语义：
//   - list：默认最近 30 条，倒序（新在前），支持 --workflow / --limit / --json
//   - show <run_id>：打印单条 Record 的详细内容（--json 直接 dump，人可读模式做基础表格）
//
// 数据源：apps/cli 的 App.History（history.JSONLStore）。History 未启用时返回明确错误。

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/workflow/history"
)

func newWorkflowHistoryCmd(h *appHolder) *cobra.Command {
	c := &cobra.Command{
		Use:   "history",
		Short: "Inspect Workflow execution history",
	}
	c.AddCommand(newWorkflowHistoryListCmd(h), newWorkflowHistoryShowCmd(h))
	return c
}

func newWorkflowHistoryListCmd(h *appHolder) *cobra.Command {
	var (
		limit   int
		wfID    string
		asJSON  bool
	)
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent Workflow runs (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			if app.History == nil {
				return fmt.Errorf("workflow history is disabled (data_dir unavailable)")
			}
			runs, err := app.History.List(cmd.Context(), history.ListOptions{
				Limit: limit, WorkflowID: wfID,
			})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(runs)
			}
			printHistoryTable(runs)
			return nil
		},
	}
	f := c.Flags()
	f.IntVar(&limit, "limit", 30, "max rows to return (hard cap 500)")
	f.StringVar(&wfID, "workflow", "", "filter by workflow id")
	f.BoolVar(&asJSON, "json", false, "print raw JSON")
	return c
}

func newWorkflowHistoryShowCmd(h *appHolder) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <run_id>",
		Short: "Show details of one Workflow run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := h.Load()
			if err != nil {
				return err
			}
			if app.History == nil {
				return fmt.Errorf("workflow history is disabled (data_dir unavailable)")
			}
			rec, err := app.History.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if rec == nil {
				return fmt.Errorf("no such run: %s", args[0])
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rec)
			}
			printHistoryDetail(rec)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print raw JSON")
	return c
}

// -----------------------------------------------------------------------------
// 人可读打印
// -----------------------------------------------------------------------------

func printHistoryTable(runs []history.Record) {
	if len(runs) == 0 {
		fmt.Fprintln(os.Stdout, "(no runs)")
		return
	}
	// 简易固定宽表格。RunID 保留 12 字符前缀 —— 完整 32 hex 太占宽。
	fmt.Printf("%-14s  %-24s  %-8s  %-10s  %-16s  %s\n",
		"RUN_ID", "WORKFLOW", "STATUS", "DURATION", "FINISHED_AT", "TARGET")
	for _, r := range runs {
		status := "ok"
		if !r.OK {
			status = "FAILED"
		}
		fmt.Printf("%-14s  %-24s  %-8s  %-10s  %-16s  %s\n",
			shortRunID(r.RunID),
			truncField(r.WorkflowID, 24),
			status,
			r.Duration.Round(time.Millisecond).String(),
			r.FinishedAt.Local().Format("01-02 15:04:05"),
			r.TargetID,
		)
	}
}

func printHistoryDetail(r *history.Record) {
	fmt.Printf("run_id:      %s\n", r.RunID)
	fmt.Printf("workflow_id: %s\n", r.WorkflowID)
	fmt.Printf("target_id:   %s\n", r.TargetID)
	fmt.Printf("caller:      %s\n", r.Caller)
	fmt.Printf("started_at:  %s\n", r.StartedAt.Local().Format(time.RFC3339))
	fmt.Printf("finished_at: %s\n", r.FinishedAt.Local().Format(time.RFC3339))
	fmt.Printf("duration:    %s\n", r.Duration)
	fmt.Printf("ok:          %v\n", r.OK)
	if r.Error != "" {
		fmt.Printf("error:       %s\n", r.Error)
	}
	fmt.Println("steps:")
	for _, s := range r.Steps {
		status := "ok"
		if s.Skipped {
			status = "skip"
		} else if !s.OK {
			status = "FAIL"
		}
		ref := s.Command
		kind := "cmd"
		if ref == "" {
			ref = s.Recipe
			kind = "recipe"
		}
		attempts := ""
		if s.Attempts > 1 {
			attempts = fmt.Sprintf(" attempts=%d", s.Attempts)
		}
		errTag := ""
		if s.ErrorCode != "" {
			errTag = fmt.Sprintf(" [%s] %s", s.ErrorCode, s.ErrorMsg)
		}
		fmt.Printf("  - %-4s %s (%s:%s) %s%s%s\n",
			status, s.StepID, kind, ref,
			s.Duration.Round(time.Millisecond),
			attempts, errTag,
		)
	}
	if len(r.Rollback) > 0 {
		fmt.Println("rollback:")
		for _, s := range r.Rollback {
			status := "ok"
			if s.Skipped {
				status = "skip" // no compensate
			} else if !s.OK {
				status = "FAIL"
			}
			ref := s.Command
			kind := "cmd"
			if ref == "" && s.Recipe != "" {
				ref = s.Recipe
				kind = "recipe"
			}
			errTag := ""
			if s.ErrorCode != "" {
				errTag = fmt.Sprintf(" [%s] %s", s.ErrorCode, s.ErrorMsg)
			}
			fmt.Printf("  - %-4s %s (%s:%s) %s%s\n",
				status, s.StepID, kind, ref,
				s.Duration.Round(time.Millisecond),
				errTag,
			)
		}
	}
}

func shortRunID(id string) string {
	if strings.HasPrefix(id, "run-") && len(id) > 16 {
		return id[:16]
	}
	if len(id) > 14 {
		return id[:14]
	}
	return id
}

func truncField(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
