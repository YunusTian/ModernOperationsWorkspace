import { useCallback, useEffect, useMemo, useState } from "react";
import { App, PluginVM, PluginHealth } from "../bindings";

// PluginsPage 展示已安装插件的元信息、健康状态，并提供启用/禁用/卸载操作。
// 后端通过 core/plugin.Lifecycle 复用 CLI 的语义。
export default function PluginsPage() {
  const [plugins, setPlugins] = useState<PluginVM[]>([]);
  const [err, setErr] = useState<string>("");
  const [busy, setBusy] = useState<string>("");
  const [selected, setSelected] = useState<string>("");
  const [confirmDelete, setConfirmDelete] = useState<{
    id: string;
    purge: boolean;
  } | null>(null);

  const load = useCallback(() => {
    setErr("");
    App.ListPlugins()
      .then((rows) => setPlugins(rows ?? []))
      .catch((e) => setErr(String(e)));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const toggle = useCallback(
    (id: string, enabled: boolean) => {
      setBusy(id);
      App.SetPluginEnabled(id, enabled)
        .then((next) => {
          setPlugins((prev) => prev.map((p) => (p.id === id ? next : p)));
        })
        .catch((e) => setErr(String(e)))
        .finally(() => setBusy(""));
    },
    [],
  );

  const doctor = useCallback(
    (id: string) => {
      setBusy(id);
      App.DoctorPlugin(id)
        .then((next) => {
          setPlugins((prev) => prev.map((p) => (p.id === id ? next : p)));
        })
        .catch((e) => setErr(String(e)))
        .finally(() => setBusy(""));
    },
    [],
  );

  const uninstall = useCallback(
    (id: string, purge: boolean) => {
      setBusy(id);
      App.UninstallPlugin(id, purge)
        .then(() => {
          setPlugins((prev) => prev.filter((p) => p.id !== id));
          if (selected === id) setSelected("");
        })
        .catch((e) => setErr(String(e)))
        .finally(() => {
          setBusy("");
          setConfirmDelete(null);
        });
    },
    [selected],
  );

  const detail = useMemo(
    () => plugins.find((p) => p.id === selected) ?? null,
    [plugins, selected],
  );

  return (
    <div className="pl-page">
      <div className="toolbar">
        <b>Plugins</b>
        <span className="dk-status">
          {plugins.length} installed · {plugins.filter((p) => p.enabled).length} enabled
        </span>
        <button onClick={load} className="secondary">
          Refresh
        </button>
      </div>
      {err && <div className="wf-error">{err}</div>}
      <div className="pl-body">
        <table className="table">
          <thead>
            <tr>
              <th>ID</th>
              <th>Name</th>
              <th>Version</th>
              <th>Platform</th>
              <th>Health</th>
              <th>Enabled</th>
              <th style={{ textAlign: "right" }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {plugins.length === 0 && (
              <tr>
                <td colSpan={7} style={{ color: "#888", padding: "20px", textAlign: "center" }}>
                  No plugins installed. Use <code>mow plugin install &lt;package&gt;</code> to add one.
                </td>
              </tr>
            )}
            {plugins.map((p) => (
              <tr
                key={p.id}
                className={selected === p.id ? "pl-row-selected" : ""}
                onClick={() => setSelected(p.id)}
              >
                <td><code>{p.id}</code></td>
                <td>{p.name || <span style={{ color: "#666" }}>—</span>}</td>
                <td>{p.version}</td>
                <td>{p.platform ? <code>{p.platform}</code> : <span style={{ color: "#a55" }}>unsupported</span>}</td>
                <td><HealthBadge health={p.health} code={p.health_code} /></td>
                <td>
                  <label className="pl-switch" onClick={(e) => e.stopPropagation()}>
                    <input
                      type="checkbox"
                      checked={p.enabled}
                      disabled={busy === p.id || p.health === "broken"}
                      onChange={(e) => toggle(p.id, e.target.checked)}
                    />
                    <span>{p.enabled ? "Enabled" : "Disabled"}</span>
                  </label>
                </td>
                <td className="actions" onClick={(e) => e.stopPropagation()}>
                  <button disabled={busy === p.id} onClick={() => doctor(p.id)}>
                    Doctor
                  </button>
                  <button
                    disabled={busy === p.id}
                    onClick={() => setConfirmDelete({ id: p.id, purge: false })}
                    className="pl-danger"
                  >
                    Uninstall
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {detail && <PluginDetail plugin={detail} />}
      </div>

      {confirmDelete && (
        <UninstallModal
          plugin={plugins.find((p) => p.id === confirmDelete.id) ?? null}
          purge={confirmDelete.purge}
          setPurge={(v) =>
            setConfirmDelete((prev) => (prev ? { ...prev, purge: v } : prev))
          }
          onCancel={() => setConfirmDelete(null)}
          onConfirm={() => uninstall(confirmDelete.id, confirmDelete.purge)}
          busy={busy === confirmDelete.id}
        />
      )}
    </div>
  );
}

function HealthBadge({ health, code }: { health: PluginHealth; code?: string }) {
  const label =
    health === "ok"
      ? "healthy"
      : health === "incompatible"
      ? "incompatible"
      : "broken";
  return (
    <span
      className={`dk-badge pl-health pl-health-${health}`}
      title={code || undefined}
    >
      {label}
    </span>
  );
}

function PluginDetail({ plugin }: { plugin: PluginVM }) {
  return (
    <div className="pl-detail">
      <div className="pl-detail-hd">
        <b>{plugin.name || plugin.id}</b>
        <span className="pl-detail-sub">
          {plugin.id}@{plugin.version}
          {plugin.author && ` · ${plugin.author}`}
        </span>
      </div>
      {plugin.description && <p className="pl-detail-desc">{plugin.description}</p>}

      <div className="pl-detail-grid">
        <div>
          <label>Package Dir</label>
          <code className="pl-code">{plugin.package_dir}</code>
        </div>
        <div>
          <label>Platform</label>
          <code className="pl-code">{plugin.platform || "unsupported"}</code>
        </div>
        <div>
          <label>Compat (core)</label>
          <code className="pl-code">{plugin.compatibility_core || "—"}</code>
        </div>
        <div>
          <label>Installed</label>
          <code className="pl-code">{plugin.installed_at || "—"}</code>
        </div>
      </div>

      {plugin.commands && plugin.commands.length > 0 && (
        <div className="pl-detail-cmds">
          <label>Commands ({plugin.commands.length})</label>
          <div>
            {plugin.commands.map((c) => (
              <span key={c} className="pill">
                {c}
              </span>
            ))}
          </div>
        </div>
      )}

      {plugin.health !== "ok" && (
        <div
          className={`pl-diagnostics ${
            plugin.health === "incompatible" ? "pl-diag-incompat" : "pl-diag-broken"
          }`}
        >
          <div className="pl-diag-hd">
            {plugin.health === "incompatible"
              ? "Compatibility check failed"
              : "Package integrity check failed"}
            {plugin.health_code && (
              <code className="pl-diag-code">{plugin.health_code}</code>
            )}
          </div>
          {plugin.health_error && <pre>{plugin.health_error}</pre>}
          {plugin.health_details && Object.keys(plugin.health_details).length > 0 && (
            <table className="pl-diag-details">
              <tbody>
                {Object.entries(plugin.health_details).map(([k, v]) => (
                  <tr key={k}>
                    <th>{k}</th>
                    <td>
                      <code>{typeof v === "object" ? JSON.stringify(v) : String(v)}</code>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}

function UninstallModal({
  plugin,
  purge,
  setPurge,
  onCancel,
  onConfirm,
  busy,
}: {
  plugin: PluginVM | null;
  purge: boolean;
  setPurge: (v: boolean) => void;
  onCancel: () => void;
  onConfirm: () => void;
  busy: boolean;
}) {
  if (!plugin) return null;
  return (
    <div className="dk-modal" onClick={onCancel}>
      <div className="dk-modal-inner" onClick={(e) => e.stopPropagation()}>
        <h3>Uninstall plugin</h3>
        <p>
          Uninstall <code>{plugin.id}@{plugin.version}</code>?
        </p>
        <label className="dk-modal-line" style={{ flexDirection: "row", alignItems: "center", gap: 6 }}>
          <input
            type="checkbox"
            checked={purge}
            onChange={(e) => setPurge(e.target.checked)}
            style={{ width: "auto" }}
          />
          <span>
            Also purge <code>.state/{plugin.id}.json</code> (removes remembered enable state)
          </span>
        </label>
        <div className="dk-modal-actions">
          <button className="secondary" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button className="dk-confirm-danger" onClick={onConfirm} disabled={busy}>
            {busy ? "Uninstalling…" : purge ? "Uninstall & Purge" : "Uninstall"}
          </button>
        </div>
      </div>
    </div>
  );
}
