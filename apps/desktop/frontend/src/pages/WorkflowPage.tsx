import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  App,
  WorkflowDoneEvent,
  WorkflowInputVM,
  WorkflowStepEvent,
  WorkflowValidateResult,
  eventsOn,
} from "../bindings";

type Props = {
  activeTarget: string;
};

type LogEntry = {
  key: string;
  stepId: string;
  ref: string;
  kind: string;
  status: "running" | "ok" | "fail" | "skipped" | "retrying";
  when?: string;
  attempts?: number;
  retryHint?: string; // 例："attempt 2/3, waiting 500ms — io error"
  durationMs?: number;
  errorCode?: string;
  errorMsg?: string;
};

// 生成新的 sessionID；使用毫秒时间戳 + 随机后缀避免碰撞。
function newSessionID(): string {
  return `wf-${Date.now()}-${Math.floor(Math.random() * 1e4)}`;
}

// 把 inputs 输入框里的字符串转成合适的 JS 类型：
//   int/bool 声明会做尝试；string/file 直接透传；解析失败退回原字符串。
function coerceInput(raw: string, type: string): unknown {
  if (type === "int") {
    const n = Number.parseInt(raw, 10);
    return Number.isFinite(n) ? n : raw;
  }
  if (type === "bool") {
    const low = raw.trim().toLowerCase();
    if (low === "true" || low === "1" || low === "yes") return true;
    if (low === "false" || low === "0" || low === "no") return false;
    return raw;
  }
  return raw;
}

export default function WorkflowPage({ activeTarget }: Props) {
  const [yamlText, setYamlText] = useState<string>("");
  const [fileName, setFileName] = useState<string>("");
  const [meta, setMeta] = useState<WorkflowValidateResult | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [runErr, setRunErr] = useState<string>("");
  const [running, setRunning] = useState<boolean>(false);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [summary, setSummary] = useState<WorkflowDoneEvent | null>(null);

  const sessionRef = useRef<string>("");
  const logsRef = useRef<HTMLDivElement | null>(null);

  // 拖入或选择文件 → 读取文本 → 调后端 validate
  const acceptFile = useCallback(async (file: File) => {
    setRunErr("");
    const text = await file.text();
    setYamlText(text);
    setFileName(file.name);
    try {
      const res = await App.WorkflowValidate(text);
      setMeta(res);
      const init: Record<string, string> = {};
      res.inputs.forEach((in_) => {
        if (in_.default !== undefined && in_.default !== null) {
          init[in_.name] = String(in_.default);
        } else {
          init[in_.name] = "";
        }
      });
      setValues(init);
      setLogs([]);
      setSummary(null);
    } catch (e) {
      setMeta(null);
      setRunErr(String(e));
    }
  }, []);

  const onDrop = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      const f = e.dataTransfer.files[0];
      if (f) void acceptFile(f);
    },
    [acceptFile],
  );

  const onPick = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const f = e.target.files?.[0];
      if (f) void acceptFile(f);
    },
    [acceptFile],
  );

  // 校验必填 inputs
  const missingRequired = useMemo(() => {
    if (!meta) return [];
    return meta.inputs
      .filter((in_) => in_.required && !(values[in_.name] ?? "").trim())
      .map((in_) => in_.name);
  }, [meta, values]);

  const runWorkflow = useCallback(async () => {
    if (!meta) return;
    if (missingRequired.length > 0) {
      setRunErr(`missing required inputs: ${missingRequired.join(", ")}`);
      return;
    }
    setRunErr("");
    setLogs([]);
    setSummary(null);
    setRunning(true);

    const coerced: Record<string, unknown> = {};
    meta.inputs.forEach((in_) => {
      const raw = values[in_.name] ?? "";
      if (raw === "") return;
      coerced[in_.name] = coerceInput(raw, in_.type);
    });

    const sess = newSessionID();
    sessionRef.current = sess;

    try {
      await App.WorkflowRun({
        session_id: sess,
        yaml: yamlText,
        target: activeTarget,
        inputs: coerced,
      });
    } catch (e) {
      setRunning(false);
      setRunErr(String(e));
    }
  }, [meta, missingRequired, values, yamlText, activeTarget]);

  // 事件订阅
  useEffect(() => {
    const sess = sessionRef.current;
    if (!sess || !running) return;

    const offStep = eventsOn(`workflow:${sess}:step`, (...data) => {
      const ev = data[0] as WorkflowStepEvent;
      setLogs((prev) => {
        const key = `${ev.index}:${ev.step_id}`;
        const next = [...prev];
        const idx = next.findIndex((x) => x.key === key);
        if (ev.phase === "start") {
          const entry: LogEntry = {
            key,
            stepId: ev.step_id,
            ref: ev.ref,
            kind: ev.kind,
            when: ev.when,
            status: "running",
          };
          if (idx >= 0) next[idx] = entry;
          else next.push(entry);
          return next;
        }
        if (idx < 0) return next;
        if (ev.phase === "retry") {
          const hint = `attempt ${ev.attempt}/${ev.max_attempts}, retry in ${
            ev.next_backoff_ms ?? 0
          }ms${ev.error_msg ? " — " + ev.error_msg : ""}`;
          next[idx] = {
            ...next[idx],
            status: "retrying",
            attempts: ev.attempt,
            retryHint: hint,
          };
          return next;
        }
        let status: LogEntry["status"];
        switch (ev.phase) {
          case "finish":
            status = "ok";
            break;
          case "skip":
            status = "skipped";
            break;
          default:
            status = "fail";
        }
        next[idx] = {
          ...next[idx],
          status,
          attempts: ev.attempts ?? next[idx].attempts,
          retryHint: undefined,
          durationMs: ev.duration_ms,
          errorCode: ev.error_code,
          errorMsg: ev.error_msg,
        };
        return next;
      });
    });

    const offDone = eventsOn(`workflow:${sess}:done`, (...data) => {
      const ev = data[0] as WorkflowDoneEvent;
      setSummary(ev);
      setRunning(false);
    });

    return () => {
      offStep();
      offDone();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running]);

  useEffect(() => {
    logsRef.current?.scrollTo({ top: logsRef.current.scrollHeight });
  }, [logs, summary]);

  return (
    <div className="wf-page">
      <h2>Workflow</h2>

      {/* 文件选择 / 拖拽区 */}
      <div
        className="wf-dropzone"
        onDragOver={(e) => e.preventDefault()}
        onDrop={onDrop}
      >
        <p>
          Drag &amp; drop a <code>.yaml</code> workflow file here, or&nbsp;
          <label className="wf-picker">
            <input type="file" accept=".yaml,.yml" onChange={onPick} />
            browse
          </label>
        </p>
        {fileName && (
          <p className="wf-file">
            📄 <b>{fileName}</b>
          </p>
        )}
      </div>

      {runErr && <div className="wf-error">{runErr}</div>}

      {/* 元数据 + inputs 表单 */}
      {meta && (
        <div className="wf-meta">
          <h3>
            {meta.id}
            {meta.name && ` — ${meta.name}`}
          </h3>
          {meta.description && (
            <pre className="wf-desc">{meta.description}</pre>
          )}
          <p className="wf-meta-line">
            steps: <b>{meta.step_count}</b>
            {"  ·  "}
            target:{" "}
            <b>
              {activeTarget || <span className="wf-warn">none selected</span>}
            </b>
          </p>

          {meta.inputs.length > 0 && (
            <table className="wf-inputs">
              <thead>
                <tr>
                  <th>name</th>
                  <th>type</th>
                  <th>value</th>
                  <th>note</th>
                </tr>
              </thead>
              <tbody>
                {meta.inputs.map((in_: WorkflowInputVM) => (
                  <tr key={in_.name}>
                    <td>
                      {in_.name}
                      {in_.required && <span className="wf-req"> *</span>}
                    </td>
                    <td>
                      <code>{in_.type || "string"}</code>
                    </td>
                    <td>
                      <input
                        value={values[in_.name] ?? ""}
                        onChange={(e) =>
                          setValues((prev) => ({
                            ...prev,
                            [in_.name]: e.target.value,
                          }))
                        }
                        placeholder={
                          in_.default !== undefined
                            ? `default: ${String(in_.default)}`
                            : ""
                        }
                      />
                    </td>
                    <td className="wf-note">{in_.description ?? ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <div className="wf-actions">
            <button
              onClick={runWorkflow}
              disabled={running || missingRequired.length > 0}
            >
              {running ? "Running…" : "▶ Run"}
            </button>
          </div>
        </div>
      )}

      {/* 日志流 */}
      {(logs.length > 0 || summary) && (
        <div className="wf-logs" ref={logsRef}>
          {logs.map((e) => (
            <div key={e.key} className={`wf-log wf-log-${e.status}`}>
              <span className="wf-icon">
                {e.status === "running"
                  ? "▶"
                  : e.status === "ok"
                  ? "✓"
                  : e.status === "skipped"
                  ? "⤼"
                  : e.status === "retrying"
                  ? "↻"
                  : "✗"}
              </span>
              <span className="wf-step">{e.stepId}</span>
              <span className="wf-ref">
                ({e.kind}:{e.ref})
              </span>
              {e.attempts !== undefined && e.attempts > 1 && (
                <span className="wf-attempts">×{e.attempts}</span>
              )}
              {e.status === "retrying" && e.retryHint && (
                <span className="wf-retry-hint">{e.retryHint}</span>
              )}
              {e.status === "skipped" && e.when && (
                <span className="wf-when">when: {e.when}</span>
              )}
              {e.durationMs !== undefined &&
                e.status !== "skipped" &&
                e.status !== "retrying" && (
                  <span className="wf-dur">{e.durationMs}ms</span>
                )}
              {e.errorCode && (
                <span className="wf-code">[{e.errorCode}]</span>
              )}
              {e.errorMsg && <span className="wf-msg">{e.errorMsg}</span>}
            </div>
          ))}
          {summary && (
            <div
              className={`wf-summary wf-log-${summary.ok ? "ok" : "fail"}`}
            >
              {summary.ok ? "✓" : "✗"} finished in {summary.duration_ms}ms
              {summary.error && <> — {summary.error}</>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
