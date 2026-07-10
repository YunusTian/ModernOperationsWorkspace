// DockerExecDrawer —— v0.3 第三阶段：容器 exec 交互式终端。
//
// 与 TerminalPage 一致的 xterm.js + base64 stdin 模型；差异：
//   - 使用 App.DockerExec* 系列 API 与 docker:exec:<sid>:* 事件
//   - 用户可自定义要执行的命令（默认 sh；shell 检测由用户手工）
//   - 关闭抽屉即自动 close 会话
import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  App,
  DockerContainerVM,
  DockerExecExitEvent,
  eventsOn,
} from "../bindings";

type Props = {
  targetID: string;
  container: DockerContainerVM;
  onClose: () => void;
};

function bytesToB64(buf: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < buf.length; i++) bin += String.fromCharCode(buf[i]);
  return btoa(bin);
}
function b64ToBytes(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// 默认命令：优先 sh -lc，比 bash 兼容度高；用户可自行改写。
const DEFAULT_CMD = "sh";

export default function DockerExecDrawer({ targetID, container, onClose }: Props) {
  const termRef = useRef<HTMLDivElement | null>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const sessionRef = useRef<string>("");
  const [status, setStatus] = useState<string>("idle");
  const [err, setErr] = useState<string>("");
  const [cmdInput, setCmdInput] = useState<string>(DEFAULT_CMD);
  const [running, setRunning] = useState<boolean>(false);

  // 拆命令：允许空格分割，兼容 'sh -lc "top -b -n 1"' 这种简单场景。
  // 为避免引入 shell-quote 依赖，此处只做朴素处理；复杂参数请用 CLI。
  const splitCmd = (raw: string): string[] => {
    const s = raw.trim();
    if (!s) return [];
    // 若整段被单/双引号包围，去掉外层引号后作为单一 argv
    if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
      return [s.slice(1, -1)];
    }
    return s.split(/\s+/);
  };

  const startExec = async () => {
    if (running) return;
    setErr("");
    setStatus("starting…");

    // 每次都重建 xterm，避免遗留状态
    if (termRef.current && !xtermRef.current) {
      const term = new Terminal({
        fontFamily:
          'Menlo, Monaco, Consolas, "Cascadia Code", "Liberation Mono", monospace',
        fontSize: 13,
        theme: { background: "#000000" },
        cursorBlink: true,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(termRef.current);
      try {
        fit.fit();
      } catch {
        /* ignore */
      }
      xtermRef.current = term;
      fitRef.current = fit;
    }
    const term = xtermRef.current!;
    term.clear();

    const cmd = splitCmd(cmdInput);
    if (cmd.length === 0) {
      setErr("command cannot be empty");
      setStatus("idle");
      return;
    }

    try {
      const sid = await App.DockerExecOpen(targetID, {
        container: container.id,
        cmd,
        tty: true,
        attach_stdin: true,
        rows: term.rows,
        cols: term.cols,
      });
      sessionRef.current = sid;
      setStatus(`connected · ${sid}`);
      setRunning(true);

      // 事件订阅
      const encoder = new TextEncoder();
      const disposers: Array<() => void> = [];
      disposers.push(
        eventsOn(`docker:exec:${sid}:stdout`, (...data: unknown[]) => {
          term.write(b64ToBytes(data[0] as string));
        }),
      );
      disposers.push(
        eventsOn(`docker:exec:${sid}:stderr`, (...data: unknown[]) => {
          term.write(b64ToBytes(data[0] as string));
        }),
      );
      disposers.push(
        eventsOn(`docker:exec:${sid}:exit`, (...data: unknown[]) => {
          const ev = (data[0] as DockerExecExitEvent) ?? {};
          setStatus(
            ev.error
              ? `exited · code=${ev.exit_code} · ${ev.error}`
              : `exited · code=${ev.exit_code}`,
          );
          setRunning(false);
          sessionRef.current = "";
          disposers.forEach((d) => d());
        }),
      );

      // stdin / resize 挂到 xterm
      term.onData((s) => {
        if (!sessionRef.current) return;
        const bytes = encoder.encode(s);
        App.DockerExecWrite(sessionRef.current, bytesToB64(bytes)).catch((e) =>
          setErr(String(e)),
        );
      });
      term.onResize(({ rows, cols }) => {
        if (!sessionRef.current) return;
        App.DockerExecResize(sessionRef.current, rows, cols).catch(() => undefined);
      });
    } catch (e) {
      setErr(String(e));
      setStatus("failed");
      setRunning(false);
    }
  };

  // 组件卸载：主动 close 会话 + dispose xterm
  useEffect(() => {
    return () => {
      const sid = sessionRef.current;
      sessionRef.current = "";
      if (sid) {
        App.DockerExecClose(sid).catch(() => undefined);
      }
      xtermRef.current?.dispose();
      xtermRef.current = null;
    };
  }, []);

  // 首次挂载后自动尝试 fit（xterm 需要在 DOM 就绪后调）
  useEffect(() => {
    const onWinResize = () => {
      try {
        fitRef.current?.fit();
      } catch {
        /* ignore */
      }
    };
    window.addEventListener("resize", onWinResize);
    return () => window.removeEventListener("resize", onWinResize);
  }, []);

  const closeAll = async () => {
    const sid = sessionRef.current;
    sessionRef.current = "";
    if (sid) {
      try {
        await App.DockerExecClose(sid);
      } catch {
        /* ignore */
      }
    }
    onClose();
  };

  return (
    <div className="dk-drawer" onClick={closeAll}>
      <div
        className="dk-drawer-inner dk-exec-inner"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="dk-drawer-hd">
          <div>
            <b>exec</b>
            <span className="wf-ref">
              {" · "}
              {container.names?.[0]?.replace(/^\//, "") ?? container.id.slice(0, 12)}
            </span>
          </div>
          <button className="secondary" onClick={closeAll}>
            Close
          </button>
        </div>
        <div className="dk-exec-toolbar">
          <label style={{ flex: 1 }}>
            command
            <input
              type="text"
              value={cmdInput}
              onChange={(e) => setCmdInput(e.target.value)}
              disabled={running}
              spellCheck={false}
            />
          </label>
          <button onClick={startExec} disabled={running}>
            {running ? "Running…" : "Start"}
          </button>
          <span className="dk-status">{status}</span>
          {err && <span className="error">{err}</span>}
        </div>
        <div className="terminal dk-exec-term" ref={termRef} />
      </div>
    </div>
  );
}
