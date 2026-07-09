import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { App, eventsOn } from "../bindings";

type Props = { targetID: string };

// base64 encode/decode utilities (浏览器原生 atob/btoa 对 UTF-8 支持不完善，用 TextEncoder)
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

export default function TerminalPage({ targetID }: Props) {
  const termRef = useRef<HTMLDivElement | null>(null);
  const xtermRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const sessionRef = useRef<string>("");
  const [status, setStatus] = useState<string>("connecting…");
  const [err, setErr] = useState<string>("");

  useEffect(() => {
    if (!termRef.current) return;

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
    fit.fit();
    xtermRef.current = term;
    fitRef.current = fit;

    const encoder = new TextEncoder();

    let disposers: Array<() => void> = [];
    let session = "";
    let mounted = true;

    (async () => {
      try {
        session = await App.ShellOpen(targetID, {
          rows: term.rows,
          cols: term.cols,
        });
        if (!mounted) {
          App.ShellClose(session).catch(() => undefined);
          return;
        }
        sessionRef.current = session;
        setStatus(`connected · ${session}`);

        disposers.push(
          eventsOn(`shell:${session}:stdout`, (...data: unknown[]) => {
            const b64 = data[0] as string;
            term.write(b64ToBytes(b64));
          }),
        );
        disposers.push(
          eventsOn(`shell:${session}:stderr`, (...data: unknown[]) => {
            const b64 = data[0] as string;
            term.write(b64ToBytes(b64));
          }),
        );
        disposers.push(
          eventsOn(`shell:${session}:exit`, (...data: unknown[]) => {
            const p = (data[0] as { exit_code?: number; error?: string }) ?? {};
            setStatus(
              p.error
                ? `exit code=${p.exit_code} error=${p.error}`
                : `exit code=${p.exit_code}`,
            );
          }),
        );

        term.onData((s) => {
          const bytes = encoder.encode(s);
          App.ShellWrite(session, bytesToB64(bytes)).catch((e) =>
            setErr(String(e)),
          );
        });
        term.onResize(({ rows, cols }) => {
          App.ShellResize(session, rows, cols).catch(() => undefined);
        });
      } catch (e) {
        setErr(String(e));
        setStatus("failed");
      }
    })();

    const onResize = () => {
      try {
        fit.fit();
      } catch {}
    };
    window.addEventListener("resize", onResize);

    return () => {
      mounted = false;
      window.removeEventListener("resize", onResize);
      disposers.forEach((d) => d());
      if (session) App.ShellClose(session).catch(() => undefined);
      term.dispose();
    };
  }, [targetID]);

  return (
    <>
      <div className="toolbar">
        <strong>{targetID}</strong>
        <span style={{ flex: 1 }} />
        <span style={{ color: "#999" }}>{status}</span>
        {err && <span className="error">{err}</span>}
      </div>
      <div className="terminal" ref={termRef} />
    </>
  );
}
