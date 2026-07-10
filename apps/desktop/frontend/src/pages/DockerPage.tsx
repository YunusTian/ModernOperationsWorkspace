// DockerPage —— Docker Dashboard 主页面（v0.3 第二阶段）
//
// 主路径（严格顺序）：
//   容器列表 (轮询 / 手动刷新)
//     └─ 点击某行 → inspect 抽屉（只读）
//     └─ 点击 "Logs" → 底部日志面板（订阅 wails 事件）
//     └─ 点击 "Start / Stop / Restart" → 弹窗二次确认 → DockerLifecycle
//
// 不做的事（第三阶段承接）：镜像 / 卷 / 网络 / Compose / rm / exec / push / pull。
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  App,
  DockerContainerVM,
  DockerLifecycleAction,
  DockerLogsExitEvent,
  eventsOn,
} from "../bindings";

type Props = { targetID: string };

type ConfirmState = {
  action: DockerLifecycleAction;
  container: DockerContainerVM;
  timeoutSec: number;
} | null;

// -----------------------------------------------------------------------------
// base64 → text 解码（复用 TerminalPage 的技巧但按 UTF-8 处理）
// -----------------------------------------------------------------------------

const utf8Dec = new TextDecoder("utf-8", { fatal: false });
function b64ToText(b64: string): string {
  const bin = atob(b64);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return utf8Dec.decode(buf);
}

// -----------------------------------------------------------------------------
// 小工具
// -----------------------------------------------------------------------------

function shortID(id: string): string {
  return id.length > 12 ? id.slice(0, 12) : id;
}
function firstName(names: string[] | undefined): string {
  if (!names || names.length === 0) return "";
  const n = names[0];
  return n.startsWith("/") ? n.slice(1) : n;
}
function stateBadgeClass(state: string): string {
  switch (state) {
    case "running":
      return "dk-badge dk-badge-running";
    case "exited":
      return "dk-badge dk-badge-exited";
    case "paused":
      return "dk-badge dk-badge-paused";
    case "restarting":
      return "dk-badge dk-badge-restarting";
    default:
      return "dk-badge";
  }
}
function isRunning(c: DockerContainerVM): boolean {
  return c.state === "running" || c.state === "restarting";
}

// -----------------------------------------------------------------------------
// 组件
// -----------------------------------------------------------------------------

export default function DockerPage({ targetID }: Props) {
  const [all, setAll] = useState<boolean>(true);
  const [containers, setContainers] = useState<DockerContainerVM[]>([]);
  const [err, setErr] = useState<string>("");
  const [loading, setLoading] = useState<boolean>(false);
  const [status, setStatus] = useState<string>("");

  // 详情 / 日志 / 确认弹窗
  const [inspectFor, setInspectFor] = useState<DockerContainerVM | null>(null);
  const [inspectRaw, setInspectRaw] = useState<string>("");
  const [inspectLoading, setInspectLoading] = useState<boolean>(false);

  const [logsFor, setLogsFor] = useState<DockerContainerVM | null>(null);
  const [logsText, setLogsText] = useState<string>("");
  const [logsSess, setLogsSess] = useState<string>("");
  const logsSessRef = useRef<string>("");
  const [follow, setFollow] = useState<boolean>(true);

  const [confirmState, setConfirmState] = useState<ConfirmState>(null);
  const [confirmBusy, setConfirmBusy] = useState<boolean>(false);

  // -------- 列表加载 --------

  const refresh = useCallback(async () => {
    setErr("");
    setLoading(true);
    try {
      const r = await App.DockerList(targetID, { all });
      setContainers(r.containers ?? []);
      setStatus(`ok · ${r.containers?.length ?? 0} containers · audit ${r.audit_id}`);
    } catch (e) {
      setErr(String(e));
      setStatus("failed");
    } finally {
      setLoading(false);
    }
  }, [targetID, all]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  // 切换 target 时清空所有派生状态
  useEffect(() => {
    setInspectFor(null);
    setInspectRaw("");
    setConfirmState(null);
    return () => {
      const sid = logsSessRef.current;
      if (sid) {
        App.DockerLogsClose(sid).catch(() => undefined);
      }
    };
  }, [targetID]);

  // -------- Inspect 抽屉 --------

  const openInspect = useCallback(
    async (c: DockerContainerVM) => {
      setInspectFor(c);
      setInspectRaw("");
      setInspectLoading(true);
      try {
        const r = await App.DockerInspect(targetID, c.id);
        setInspectRaw(JSON.stringify(r.raw, null, 2));
      } catch (e) {
        setInspectRaw(`error: ${String(e)}`);
      } finally {
        setInspectLoading(false);
      }
    },
    [targetID],
  );

  // -------- Logs 面板 --------

  const closeLogs = useCallback(async () => {
    const sid = logsSessRef.current;
    logsSessRef.current = "";
    setLogsSess("");
    if (sid) {
      try {
        await App.DockerLogsClose(sid);
      } catch {
        /* ignore */
      }
    }
  }, []);

  const openLogs = useCallback(
    async (c: DockerContainerVM) => {
      await closeLogs();
      setLogsFor(c);
      setLogsText("");
      try {
        const sid = await App.DockerLogsOpen(targetID, {
          container: c.id,
          follow,
          tail: "200",
        });
        logsSessRef.current = sid;
        setLogsSess(sid);
      } catch (e) {
        setLogsText(`error: ${String(e)}\n`);
      }
    },
    [closeLogs, follow, targetID],
  );

  // 订阅当前 log session 的三类事件
  useEffect(() => {
    if (!logsSess) return;
    const disposers: Array<() => void> = [];
    const append = (chunk: string) =>
      setLogsText((prev) => (prev + chunk).slice(-200_000)); // 200KB 上限

    disposers.push(
      eventsOn(`docker:logs:${logsSess}:stdout`, (...data: unknown[]) => {
        append(b64ToText(data[0] as string));
      }),
    );
    disposers.push(
      eventsOn(`docker:logs:${logsSess}:stderr`, (...data: unknown[]) => {
        append(b64ToText(data[0] as string));
      }),
    );
    disposers.push(
      eventsOn(`docker:logs:${logsSess}:exit`, (...data: unknown[]) => {
        const ev = (data[0] as DockerLogsExitEvent) ?? {};
        if (ev.error) append(`\n[stream ended] ${ev.error}\n`);
        else append(`\n[stream ended]\n`);
        logsSessRef.current = "";
        setLogsSess("");
      }),
    );
    return () => disposers.forEach((d) => d());
  }, [logsSess]);

  // -------- 生命周期动作 二次确认 --------

  const askConfirm = (action: DockerLifecycleAction, c: DockerContainerVM) => {
    setConfirmState({
      action,
      container: c,
      timeoutSec: action === "stop" || action === "restart" ? 10 : 0,
    });
  };

  const doConfirm = async () => {
    if (!confirmState) return;
    setConfirmBusy(true);
    try {
      const r = await App.DockerLifecycle(targetID, {
        action: confirmState.action,
        container: confirmState.container.id,
        timeout_sec:
          confirmState.timeoutSec > 0 ? confirmState.timeoutSec : undefined,
        confirmed: true,
      });
      setStatus(
        r.already_in_state
          ? `${r.action} · already in state · audit ${r.audit_id}`
          : `${r.action} ok · audit ${r.audit_id}`,
      );
      setConfirmState(null);
      // 刷新一次列表让状态变化立即可见
      refresh();
    } catch (e) {
      setErr(String(e));
    } finally {
      setConfirmBusy(false);
    }
  };

  const canStart = (c: DockerContainerVM) => c.state === "exited" || c.state === "created" || c.state === "paused" || c.state === "dead";
  const canStop = (c: DockerContainerVM) => isRunning(c);
  const canRestart = (c: DockerContainerVM) => isRunning(c);

  // -------- 视图 --------

  const rows = useMemo(() => containers, [containers]);

  return (
    <>
      <div className="toolbar">
        <button onClick={refresh} disabled={loading}>
          {loading ? "Loading…" : "Refresh"}
        </button>
        <label className="dk-check">
          <input
            type="checkbox"
            checked={all}
            onChange={(e) => setAll(e.target.checked)}
          />
          Show all (incl. exited)
        </label>
        <span style={{ flex: 1 }} />
        <span className="dk-status">{status}</span>
        {err && <span className="error">{err}</span>}
      </div>

      <div className="content dk-content">
        {rows.length === 0 ? (
          <p style={{ color: "#888" }}>
            {loading ? "Loading containers…" : "No containers on this target."}
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th style={{ width: 90 }}>State</th>
                <th>Name</th>
                <th>Image</th>
                <th>ID</th>
                <th>Ports</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((c) => (
                <tr key={c.id} onClick={() => openInspect(c)} style={{ cursor: "pointer" }}>
                  <td>
                    <span className={stateBadgeClass(c.state)}>{c.state}</span>
                  </td>
                  <td>{firstName(c.names)}</td>
                  <td className="dk-image">{c.image}</td>
                  <td className="dk-id">{shortID(c.id)}</td>
                  <td>
                    {(c.ports ?? []).map((p, i) => (
                      <span key={i} className="pill">
                        {p.public_port
                          ? `${p.public_port}→${p.private_port}/${p.type ?? "tcp"}`
                          : `${p.private_port}/${p.type ?? "tcp"}`}
                      </span>
                    ))}
                  </td>
                  <td className="dk-status-cell">{c.status ?? ""}</td>
                  <td className="actions" onClick={(e) => e.stopPropagation()}>
                    <button
                      disabled={!canStart(c)}
                      onClick={() => askConfirm("start", c)}
                    >
                      Start
                    </button>
                    <button
                      disabled={!canRestart(c)}
                      onClick={() => askConfirm("restart", c)}
                    >
                      Restart
                    </button>
                    <button
                      disabled={!canStop(c)}
                      onClick={() => askConfirm("stop", c)}
                    >
                      Stop
                    </button>
                    <button onClick={() => openLogs(c)}>Logs</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Inspect 抽屉 */}
      {inspectFor && (
        <div className="dk-drawer" onClick={() => setInspectFor(null)}>
          <div className="dk-drawer-inner" onClick={(e) => e.stopPropagation()}>
            <div className="dk-drawer-hd">
              <div>
                <span className={stateBadgeClass(inspectFor.state)}>
                  {inspectFor.state}
                </span>{" "}
                <b>{firstName(inspectFor.names)}</b>
                <span className="wf-ref">
                  {" · "}
                  {shortID(inspectFor.id)}
                </span>
              </div>
              <button className="secondary" onClick={() => setInspectFor(null)}>
                Close
              </button>
            </div>
            <div className="dk-drawer-body">
              {inspectLoading ? (
                <p style={{ color: "#888" }}>Loading inspect…</p>
              ) : (
                <pre className="dk-inspect">{inspectRaw}</pre>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Logs 面板：置底，非 modal */}
      {logsFor && (
        <div className="dk-logs-panel">
          <div className="dk-logs-hd">
            <div>
              <b>Logs</b>
              <span className="wf-ref">
                {" · "}
                {firstName(logsFor.names)} ({shortID(logsFor.id)}){logsSess && " · streaming"}
              </span>
            </div>
            <label className="dk-check">
              <input
                type="checkbox"
                checked={follow}
                onChange={(e) => setFollow(e.target.checked)}
                disabled={!!logsSess}
              />
              Follow
            </label>
            <button
              className="secondary"
              onClick={() => openLogs(logsFor)}
              disabled={!!logsSess}
            >
              Restart stream
            </button>
            <button
              className="secondary"
              onClick={async () => {
                await closeLogs();
                setLogsFor(null);
                setLogsText("");
              }}
            >
              Close
            </button>
          </div>
          <pre className="dk-logs">{logsText || "(no output yet)"}</pre>
        </div>
      )}

      {/* 二次确认弹窗 */}
      {confirmState && (
        <div className="dk-modal" onClick={() => !confirmBusy && setConfirmState(null)}>
          <div className="dk-modal-inner" onClick={(e) => e.stopPropagation()}>
            <h3>
              Confirm docker.<b>{confirmState.action}</b>
            </h3>
            <p className="wf-note">
              This will run <code>docker.{confirmState.action}</code> on{" "}
              <b>{firstName(confirmState.container.names)}</b> (
              {shortID(confirmState.container.id)}) via the connected Docker engine.
            </p>
            {(confirmState.action === "stop" ||
              confirmState.action === "restart") && (
              <label className="dk-modal-line">
                Grace period (seconds before SIGKILL)
                <input
                  type="number"
                  min={0}
                  value={confirmState.timeoutSec}
                  onChange={(e) =>
                    setConfirmState({
                      ...confirmState,
                      timeoutSec: Math.max(0, Number(e.target.value) || 0),
                    })
                  }
                />
              </label>
            )}
            <div className="dk-modal-actions">
              <button
                className="secondary"
                onClick={() => setConfirmState(null)}
                disabled={confirmBusy}
              >
                Cancel
              </button>
              <button
                onClick={doConfirm}
                disabled={confirmBusy}
                className="dk-confirm-danger"
              >
                {confirmBusy ? "Working…" : `Confirm ${confirmState.action}`}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
