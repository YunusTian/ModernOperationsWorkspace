// WorkflowHistoryPanel —— WorkflowPage 底部的执行历史面板（v0.3 第三批）。
//
// 定位：观测入口，不承担二次执行。
//   - 列表默认展示最近 30 条
//   - 点击某行 → 展开 Detail 抽屉（inputs / steps / audit id / 错误码）
//   - "Refresh" 手动刷新；WorkflowRun 完成后由父组件传 refreshTick 触发
import { useCallback, useEffect, useState } from "react";
import {
  App,
  WorkflowRunDetail,
  WorkflowRunRow,
} from "../bindings";

type Props = {
  // 父组件每次 WorkflowRun 完成后自增；用于触发本组件重新拉取历史。
  refreshTick: number;
};

function statusIcon(row: WorkflowRunRow): string {
  if (!row.ok) return "✗";
  if ((row.skipped_count ?? 0) > 0 || (row.retried_count ?? 0) > 0) return "◐";
  return "✓";
}

function formatDur(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(2)}s`;
  const m = Math.floor(s / 60);
  const rem = s - m * 60;
  return `${m}m${rem.toFixed(0)}s`;
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

export default function WorkflowHistoryPanel({ refreshTick }: Props) {
  const [rows, setRows] = useState<WorkflowRunRow[]>([]);
  const [err, setErr] = useState<string>("");
  const [loading, setLoading] = useState<boolean>(false);
  const [open, setOpen] = useState<boolean>(true);
  const [detail, setDetail] = useState<WorkflowRunDetail | null>(null);
  const [detailErr, setDetailErr] = useState<string>("");

  const refresh = useCallback(async () => {
    setErr("");
    setLoading(true);
    try {
      const list = await App.ListWorkflowRuns({ limit: 30 });
      setRows(list ?? []);
    } catch (e) {
      setErr(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh, refreshTick]);

  const openDetail = useCallback(async (runID: string) => {
    setDetail(null);
    setDetailErr("");
    try {
      const d = await App.GetWorkflowRun(runID);
      setDetail(d);
    } catch (e) {
      setDetailErr(String(e));
    }
  }, []);

  return (
    <div className="wf-history">
      <div className="wf-history-hd">
        <button
          className="wf-history-toggle"
          onClick={() => setOpen((v) => !v)}
          title={open ? "Collapse" : "Expand"}
        >
          {open ? "▾" : "▸"} History
        </button>
        <span className="wf-history-count">{rows.length}</span>
        <button className="secondary" onClick={refresh} disabled={loading}>
          {loading ? "…" : "Refresh"}
        </button>
        {err && <span className="error">{err}</span>}
      </div>

      {open && (
        <>
          {rows.length === 0 ? (
            <p style={{ color: "#888", margin: "8px 0" }}>
              {loading ? "Loading…" : "No runs yet."}
            </p>
          ) : (
            <table className="table wf-history-table">
              <thead>
                <tr>
                  <th style={{ width: 32 }}></th>
                  <th>Workflow</th>
                  <th>Target</th>
                  <th>Duration</th>
                  <th>Finished</th>
                  <th>Steps</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr
                    key={r.run_id}
                    className={r.ok ? "" : "wf-log-fail"}
                    onClick={() => openDetail(r.run_id)}
                    style={{ cursor: "pointer" }}
                  >
                    <td className="wf-history-icon">{statusIcon(r)}</td>
                    <td>{r.workflow_id}</td>
                    <td>{r.target_id ?? ""}</td>
                    <td>{formatDur(r.duration_ms)}</td>
                    <td>{formatTime(r.finished_at)}</td>
                    <td>
                      {r.step_count}
                      {r.skipped_count ? ` · ⤼${r.skipped_count}` : ""}
                      {r.retried_count ? ` · ↻${r.retried_count}` : ""}
                    </td>
                    <td className="wf-history-id">{r.run_id.slice(0, 12)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}

      {(detail || detailErr) && (
        <div
          className="dk-drawer"
          onClick={() => {
            setDetail(null);
            setDetailErr("");
          }}
        >
          <div className="dk-drawer-inner" onClick={(e) => e.stopPropagation()}>
            <div className="dk-drawer-hd">
              <div>
                <b>{detail?.row.workflow_id ?? "run"}</b>
                <span className="wf-ref"> · {detail?.row.run_id}</span>
              </div>
              <button
                className="secondary"
                onClick={() => {
                  setDetail(null);
                  setDetailErr("");
                }}
              >
                Close
              </button>
            </div>
            <div className="dk-drawer-body">
              {detailErr && <div className="error">{detailErr}</div>}
              {detail && (
                <>
                  <p className="wf-note">
                    <b>Status:</b>{" "}
                    {detail.row.ok ? (
                      <span style={{ color: "#6f6" }}>ok</span>
                    ) : (
                      <span style={{ color: "#f66" }}>failed</span>
                    )}{" "}
                    · <b>Duration:</b> {formatDur(detail.row.duration_ms)} ·{" "}
                    <b>Target:</b> {detail.row.target_id || "(none)"} ·{" "}
                    <b>Caller:</b> {detail.row.caller || "(unknown)"}
                  </p>
                  {detail.row.error && (
                    <p className="error">Error: {detail.row.error}</p>
                  )}
                  <h4 style={{ marginTop: 12 }}>Inputs</h4>
                  <pre className="dk-inspect">
                    {JSON.stringify(detail.inputs ?? {}, null, 2)}
                  </pre>
                  <h4 style={{ marginTop: 12 }}>Steps</h4>
                  <table className="table">
                    <thead>
                      <tr>
                        <th>#</th>
                        <th>step_id</th>
                        <th>ref</th>
                        <th>duration</th>
                        <th>attempts</th>
                        <th>status</th>
                      </tr>
                    </thead>
                    <tbody>
                      {(detail.steps ?? []).map((s, i) => {
                        const ref = s.command || s.recipe || "";
                        let status: string;
                        if (s.skipped) status = "skipped";
                        else if (s.ok) status = "ok";
                        else status = "FAIL";
                        return (
                          <tr key={i}>
                            <td>{i + 1}</td>
                            <td>{s.step_id}</td>
                            <td>
                              {s.command ? "cmd" : "recipe"}:{ref}
                            </td>
                            <td>{formatDur(s.duration_ms)}</td>
                            <td>{s.attempts ?? ""}</td>
                            <td>
                              {status}
                              {s.error_code && ` [${s.error_code}]`}
                              {s.error_msg && `: ${s.error_msg}`}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
