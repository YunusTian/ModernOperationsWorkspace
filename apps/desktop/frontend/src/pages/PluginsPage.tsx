import { useCallback, useEffect, useMemo, useState } from "react";
import {
  App,
  PluginVM,
  PluginHealth,
  CatalogEntry,
  CatalogRelease,
  CatalogSearchResultVM,
  CatalogRefreshResultVM,
  CatalogSourceVM,
} from "../bindings";

// PluginsPage 提供两个标签页：
//   - Installed：已安装插件的健康状态与启停 / 卸载 / 诊断
//   - Marketplace：从 Catalog 搜索、刷新、安装 / 升级
type PluginsTab = "installed" | "marketplace";

export default function PluginsPage() {
  const [tab, setTab] = useState<PluginsTab>("installed");
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
        <div className="dk-tabs">
          <button
            className={`dk-tab ${tab === "installed" ? "active" : ""}`}
            onClick={() => setTab("installed")}
          >
            Installed
          </button>
          <button
            className={`dk-tab ${tab === "marketplace" ? "active" : ""}`}
            onClick={() => setTab("marketplace")}
          >
            Marketplace
          </button>
        </div>
        {tab === "installed" && (
          <>
            <span className="dk-status">
              {plugins.length} installed · {plugins.filter((p) => p.enabled).length} enabled
            </span>
            <button onClick={load} className="secondary">
              Refresh
            </button>
          </>
        )}
      </div>
      {err && <div className="wf-error">{err}</div>}
      {tab === "installed" ? (
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
                    No plugins installed. Switch to <b>Marketplace</b> to install one from the catalog.
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
      ) : (
        <MarketplacePane
          installed={plugins}
          onInstalled={load}
          onError={setErr}
        />
      )}

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

// -----------------------------------------------------------------------------
// Marketplace 面板：搜索 catalog、按平台过滤，一键 install / update
// -----------------------------------------------------------------------------

function MarketplacePane({
  installed,
  onInstalled,
  onError,
}: {
  installed: PluginVM[];
  onInstalled: () => void;
  onError: (e: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<CatalogSearchResultVM[] | null>(null);
  const [refreshRows, setRefreshRows] = useState<CatalogRefreshResultVM[] | null>(null);
  const [sources, setSources] = useState<CatalogSourceVM[]>([]);
  const [busy, setBusy] = useState<string>("");
  const [message, setMessage] = useState<string>("");
  const [selectedVersions, setSelectedVersions] = useState<Record<string, string>>({});

  // 有些插件同源存在多个可选版本，本 map 记录 UI 选择。
  const installedByID = useMemo(() => {
    const m: Record<string, PluginVM> = {};
    for (const p of installed) m[p.id] = p;
    return m;
  }, [installed]);

  const loadSources = useCallback(() => {
    App.ListCatalogSources()
      .then((rows) => setSources(rows ?? []))
      .catch((e) => onError(String(e)));
  }, [onError]);

  useEffect(() => {
    loadSources();
    // 首次进入自动搜一次（q="" 相当于 list all）
    App.SearchCatalog("").then(setResults).catch((e) => onError(String(e)));
  }, [loadSources, onError]);

  const search = useCallback(
    (q: string) => {
      setBusy("search");
      setMessage("");
      App.SearchCatalog(q)
        .then(setResults)
        .catch((e) => onError(String(e)))
        .finally(() => setBusy(""));
    },
    [onError],
  );

  const refresh = useCallback(() => {
    setBusy("refresh");
    setMessage("");
    App.RefreshCatalog(true)
      .then((rows) => {
        setRefreshRows(rows);
        return App.SearchCatalog(query);
      })
      .then((r) => r && setResults(r))
      .catch((e) => onError(String(e)))
      .finally(() => {
        setBusy("");
        loadSources();
      });
  }, [query, onError, loadSources]);

  const install = useCallback(
    (id: string, version: string, update: boolean) => {
      const key = `${update ? "update" : "install"}:${id}`;
      setBusy(key);
      setMessage("");
      const call = update
        ? App.UpdatePluginFromCatalog({ id, version })
        : App.InstallPluginFromCatalog({ id, version });
      call
        .then((vm) => {
          setMessage(`${update ? "Updated" : "Installed"} ${vm.id}@${vm.version}`);
          onInstalled();
        })
        .catch((e) => onError(String(e)))
        .finally(() => setBusy(""));
    },
    [onError, onInstalled],
  );

  // 把结果打平成 Entry 列表，同 id 合并各源版本；同版本仅保留一个。
  const merged: CatalogEntry[] = useMemo(() => {
    const m: Record<string, CatalogEntry> = {};
    for (const r of results ?? []) {
      if (!r.entries) continue;
      for (const e of r.entries) {
        const prev = m[e.id];
        if (!prev) {
          m[e.id] = { ...e, versions: [...e.versions] };
          continue;
        }
        const seenVers = new Set(prev.versions.map((v) => v.version));
        for (const v of e.versions) {
          if (!seenVers.has(v.version)) prev.versions.push(v);
        }
      }
    }
    return Object.values(m).sort((a, b) => a.id.localeCompare(b.id));
  }, [results]);

  return (
    <div className="pl-body">
      <div className="pl-market-toolbar">
        <input
          type="text"
          placeholder="Search catalog (id, name, tag)…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") search(query);
          }}
        />
        <button onClick={() => search(query)} disabled={busy === "search"}>
          Search
        </button>
        <button className="secondary" onClick={refresh} disabled={busy === "refresh"}>
          {busy === "refresh" ? "Refreshing…" : "Refresh"}
        </button>
        <span className="dk-status">
          {sources.length} source{sources.length === 1 ? "" : "s"}
        </span>
      </div>

      {message && <div className="pl-market-msg">{message}</div>}

      {refreshRows && (
        <div className="pl-market-refresh">
          {refreshRows.map((r) => (
            <div key={r.source} className={r.ok ? "pl-market-src-ok" : "pl-market-src-err"}>
              <b>{r.source}</b>
              {r.ok
                ? ` — ${r.num_entries ?? 0} entries`
                : ` — ${r.error ?? "failed"}`}
            </div>
          ))}
        </div>
      )}

      {sources.length === 0 && (
        <div className="pl-market-empty">
          No catalog sources configured. Add one to <code>app.catalog.sources</code>{" "}
          in your MOW config.
        </div>
      )}

      <table className="table">
        <thead>
          <tr>
            <th>ID</th>
            <th>Name</th>
            <th>Latest</th>
            <th>Installed</th>
            <th>Version</th>
            <th style={{ textAlign: "right" }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {merged.length === 0 && sources.length > 0 && (
            <tr>
              <td colSpan={6} style={{ color: "#888", padding: "20px", textAlign: "center" }}>
                No matching plugins.
              </td>
            </tr>
          )}
          {merged.map((entry) => {
            const latest = entry.versions[0];
            const inst = installedByID[entry.id];
            const chosen = selectedVersions[entry.id] ?? latest.version;
            const chosenRel = entry.versions.find((v) => v.version === chosen) ?? latest;
            const alreadyThisVersion = inst && inst.version === chosen;
            const outdated = inst && inst.version !== latest.version;
            const canUpdate = !!inst && !alreadyThisVersion;
            const canInstall = !inst;
            const installBusy =
              busy === `install:${entry.id}` || busy === `update:${entry.id}`;
            return (
              <tr key={entry.id}>
                <td><code>{entry.id}</code></td>
                <td>{entry.name || <span style={{ color: "#666" }}>—</span>}</td>
                <td>
                  <code>{latest.version}</code>
                  {latest.compatibility.core && (
                    <span className="pl-market-compat" title={`core ${latest.compatibility.core}`}>
                      core {latest.compatibility.core}
                    </span>
                  )}
                </td>
                <td>
                  {inst ? (
                    <span className={outdated ? "pl-market-outdated" : ""}>
                      <code>{inst.version}</code>
                      {outdated && <span className="pl-market-tag">update available</span>}
                    </span>
                  ) : (
                    <span style={{ color: "#666" }}>—</span>
                  )}
                </td>
                <td>
                  <select
                    value={chosen}
                    onChange={(e) =>
                      setSelectedVersions((prev) => ({
                        ...prev,
                        [entry.id]: e.target.value,
                      }))
                    }
                  >
                    {entry.versions.map((v) => (
                      <option key={v.version} value={v.version}>
                        {v.version}
                        {v.yanked ? " (yanked)" : ""}
                      </option>
                    ))}
                  </select>
                </td>
                <td className="actions">
                  {canInstall && (
                    <button
                      disabled={installBusy}
                      onClick={() => install(entry.id, chosenRel.version, false)}
                    >
                      {installBusy ? "…" : "Install"}
                    </button>
                  )}
                  {canUpdate && (
                    <button
                      disabled={installBusy}
                      onClick={() => install(entry.id, chosenRel.version, true)}
                    >
                      {installBusy ? "…" : "Update"}
                    </button>
                  )}
                  {alreadyThisVersion && (
                    <span className="pl-market-installed">Installed</span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>

      <ReleaseDetails entries={merged} selectedVersions={selectedVersions} />
    </div>
  );
}

function ReleaseDetails({
  entries,
  selectedVersions,
}: {
  entries: CatalogEntry[];
  selectedVersions: Record<string, string>;
}) {
  const [openID, setOpenID] = useState<string>("");
  const entry = entries.find((e) => e.id === openID);
  const release: CatalogRelease | undefined = useMemo(() => {
    if (!entry) return undefined;
    const chosen = selectedVersions[entry.id] ?? entry.versions[0].version;
    return entry.versions.find((v) => v.version === chosen);
  }, [entry, selectedVersions]);
  return (
    <div className="pl-market-details">
      <div className="pl-market-details-hd">
        <label>Details</label>
        <select value={openID} onChange={(e) => setOpenID(e.target.value)}>
          <option value="">— pick a plugin —</option>
          {entries.map((e) => (
            <option key={e.id} value={e.id}>
              {e.id}
            </option>
          ))}
        </select>
      </div>
      {entry && release && (
        <div>
          {entry.description && <p className="pl-detail-desc">{entry.description}</p>}
          {release.releaseNotes && (
            <pre className="pl-market-notes">{release.releaseNotes}</pre>
          )}
          <table className="pl-diag-details">
            <tbody>
              <tr>
                <th>id</th>
                <td><code>{entry.id}</code></td>
              </tr>
              <tr>
                <th>version</th>
                <td><code>{release.version}</code></td>
              </tr>
              {entry.author && (
                <tr>
                  <th>author</th>
                  <td>{entry.author}</td>
                </tr>
              )}
              {entry.homepage && (
                <tr>
                  <th>homepage</th>
                  <td><code>{entry.homepage}</code></td>
                </tr>
              )}
              {release.compatibility.core && (
                <tr>
                  <th>compat (core)</th>
                  <td><code>{release.compatibility.core}</code></td>
                </tr>
              )}
              <tr>
                <th>platforms</th>
                <td>
                  {release.platforms.map((p) => (
                    <span key={`${p.os}-${p.arch}`} className="pill">
                      {p.os}/{p.arch}
                    </span>
                  ))}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
