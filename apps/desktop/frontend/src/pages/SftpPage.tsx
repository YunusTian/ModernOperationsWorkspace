import { useEffect, useState } from "react";
import { App, SFTPEntry } from "../bindings";

type Props = { targetID: string };

function joinPath(base: string, name: string): string {
  if (base === "" || base === "/") return "/" + name;
  return base.replace(/\/$/, "") + "/" + name;
}
function parentPath(p: string): string {
  if (!p || p === "/" || p === ".") return p;
  const trimmed = p.replace(/\/+$/, "");
  const i = trimmed.lastIndexOf("/");
  if (i <= 0) return "/";
  return trimmed.slice(0, i);
}

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} K`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} M`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} G`;
}

export default function SftpPage({ targetID }: Props) {
  const [path, setPath] = useState<string>(".");
  const [pathInput, setPathInput] = useState<string>(".");
  const [entries, setEntries] = useState<SFTPEntry[]>([]);
  const [err, setErr] = useState<string>("");
  const [busy, setBusy] = useState<string>("");

  const load = (p: string) => {
    setErr("");
    setBusy("list");
    App.SftpList(targetID, p)
      .then((r) => {
        setEntries(r.entries ?? []);
        setPath(r.path);
        setPathInput(r.path);
      })
      .catch((e) => setErr(String(e)))
      .finally(() => setBusy(""));
  };

  useEffect(() => {
    load(".");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetID]);

  const onGo = () => load(pathInput);
  const onUp = () => load(parentPath(path));
  const onEnter = (e: SFTPEntry) => {
    if (!e.is_dir) return;
    load(joinPath(path, e.name));
  };

  const onDownload = async (e: SFTPEntry) => {
    const remote = joinPath(path, e.name);
    const local = prompt("Save to (local absolute path):", e.name);
    if (!local) return;
    setBusy(e.name);
    setErr("");
    try {
      await App.SftpDownload(targetID, remote, local);
      alert(`Downloaded to ${local}`);
    } catch (err) {
      setErr(String(err));
    } finally {
      setBusy("");
    }
  };

  const onUpload = async () => {
    const local = prompt("Local absolute path to upload:");
    if (!local) return;
    const base = local.replace(/\\/g, "/").split("/").pop() ?? "upload.bin";
    const remoteDefault = joinPath(path, base);
    const remote = prompt("Remote destination:", remoteDefault);
    if (!remote) return;
    setBusy("upload");
    setErr("");
    try {
      await App.SftpUpload(targetID, local, remote);
      load(path);
    } catch (err) {
      setErr(String(err));
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <div className="toolbar">
        <strong>{targetID}</strong>
        <button className="secondary" onClick={onUp} disabled={!!busy}>
          ↑ Up
        </button>
        <input
          value={pathInput}
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onGo();
          }}
          style={{ flex: 1 }}
        />
        <button onClick={onGo} disabled={!!busy}>
          Go
        </button>
        <button className="secondary" onClick={onUpload} disabled={!!busy}>
          Upload
        </button>
        <button className="secondary" onClick={() => load(path)} disabled={!!busy}>
          Refresh
        </button>
      </div>
      {err && <div className="status error">{err}</div>}
      <div className="content">
        <table className="table">
          <thead>
            <tr>
              <th style={{ width: "50%" }}>Name</th>
              <th>Size</th>
              <th>Mode</th>
              <th>Modified</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr
                key={e.name}
                onDoubleClick={() => onEnter(e)}
                style={{ cursor: e.is_dir ? "pointer" : "default" }}
              >
                <td>
                  {e.is_dir ? "📁 " : e.is_link ? "🔗 " : "📄 "}
                  {e.name}
                </td>
                <td>{e.is_dir ? "" : formatSize(e.size)}</td>
                <td style={{ fontFamily: "monospace", fontSize: 12 }}>
                  {e.mode}
                </td>
                <td style={{ fontSize: 12, color: "#999" }}>{e.mod_time}</td>
                <td className="actions">
                  {!e.is_dir && (
                    <button
                      onClick={() => onDownload(e)}
                      disabled={busy === e.name}
                    >
                      {busy === e.name ? "..." : "Download"}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="status">{busy ? `busy: ${busy}` : `path: ${path}`}</div>
    </>
  );
}
