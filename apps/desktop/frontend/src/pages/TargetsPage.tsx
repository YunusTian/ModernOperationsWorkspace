import { useEffect, useState } from "react";
import { App, TargetVM, UpsertSSHTargetInput } from "../bindings";

type Props = {
  active: string;
  onPick: (id: string) => void;
  onOpenTerminal: (id: string) => void;
  onOpenSftp: (id: string) => void;
};

const emptyForm: UpsertSSHTargetInput = {
  id: "",
  name: "",
  host: "",
  port: 22,
  user: "",
  method: "password",
  password: "",
  private_key: "",
  passphrase: "",
  known_hosts_mode: "accept-new",
};

export default function TargetsPage(props: Props) {
  const [targets, setTargets] = useState<TargetVM[]>([]);
  const [err, setErr] = useState<string>("");
  const [busy, setBusy] = useState<string>("");
  const [form, setForm] = useState<UpsertSSHTargetInput>(emptyForm);
  const [showForm, setShowForm] = useState(false);

  const refresh = () => {
    setErr("");
    App.ListTargets()
      .then(setTargets)
      .catch((e) => setErr(String(e)));
  };
  useEffect(refresh, []);

  const onSubmit = async () => {
    if (!form.id || !form.host || !form.user) {
      setErr("id / host / user 必填");
      return;
    }
    try {
      await App.UpsertSSHTarget(form);
      setShowForm(false);
      setForm(emptyForm);
      refresh();
    } catch (e) {
      setErr(String(e));
    }
  };

  const onDelete = async (id: string) => {
    if (!confirm(`Delete target "${id}"?`)) return;
    try {
      await App.DeleteTarget(id);
      refresh();
    } catch (e) {
      setErr(String(e));
    }
  };

  const onPing = async (id: string) => {
    setBusy(id);
    setErr("");
    try {
      const audit = await App.PingTarget(id);
      alert(`OK (audit=${audit})`);
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <div className="toolbar">
        <button onClick={() => setShowForm((s) => !s)}>
          {showForm ? "Cancel" : "+ Add SSH Target"}
        </button>
        <button className="secondary" onClick={refresh}>
          Refresh
        </button>
        <span style={{ flex: 1 }} />
        {err && <span className="error">{err}</span>}
      </div>

      {showForm && (
        <div className="form">
          <label>
            ID
            <input
              value={form.id}
              onChange={(e) => setForm({ ...form, id: e.target.value })}
              placeholder="e.g. srv-web-01"
            />
          </label>
          <label>
            Name
            <input
              value={form.name ?? ""}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </label>
          <label>
            Host
            <input
              value={form.host}
              onChange={(e) => setForm({ ...form, host: e.target.value })}
            />
          </label>
          <label>
            Port
            <input
              type="number"
              value={form.port ?? 22}
              onChange={(e) =>
                setForm({ ...form, port: Number(e.target.value) })
              }
            />
          </label>
          <label>
            User
            <input
              value={form.user}
              onChange={(e) => setForm({ ...form, user: e.target.value })}
            />
          </label>
          <label>
            Auth Method
            <select
              value={form.method}
              onChange={(e) =>
                setForm({
                  ...form,
                  method: e.target.value as UpsertSSHTargetInput["method"],
                })
              }
            >
              <option value="password">password</option>
              <option value="privatekey">privatekey</option>
              <option value="agent">agent</option>
            </select>
          </label>
          {form.method === "password" && (
            <label>
              Password
              <input
                type="password"
                value={form.password ?? ""}
                onChange={(e) =>
                  setForm({ ...form, password: e.target.value })
                }
              />
            </label>
          )}
          {form.method === "privatekey" && (
            <>
              <label style={{ gridColumn: "1/-1" }}>
                Private Key (PEM)
                <textarea
                  rows={4}
                  value={form.private_key ?? ""}
                  onChange={(e) =>
                    setForm({ ...form, private_key: e.target.value })
                  }
                />
              </label>
              <label>
                Passphrase (optional)
                <input
                  type="password"
                  value={form.passphrase ?? ""}
                  onChange={(e) =>
                    setForm({ ...form, passphrase: e.target.value })
                  }
                />
              </label>
            </>
          )}
          <label>
            known_hosts mode
            <select
              value={form.known_hosts_mode ?? "accept-new"}
              onChange={(e) =>
                setForm({
                  ...form,
                  known_hosts_mode: e.target
                    .value as UpsertSSHTargetInput["known_hosts_mode"],
                })
              }
            >
              <option value="accept-new">accept-new</option>
              <option value="strict">strict</option>
              <option value="insecure-ignore">insecure-ignore</option>
            </select>
          </label>
          <div className="form-actions">
            <button onClick={onSubmit}>Save</button>
            <button
              className="secondary"
              onClick={() => {
                setShowForm(false);
                setForm(emptyForm);
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="content">
        {targets.length === 0 ? (
          <p style={{ color: "#888" }}>暂无 Target。点击 "+ Add SSH Target" 新建。</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Type</th>
                <th>Host</th>
                <th>User</th>
                <th>Tags</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {targets.map((t) => (
                <tr
                  key={t.id}
                  onClick={() => props.onPick(t.id)}
                  style={{
                    background: t.id === props.active ? "#094771" : undefined,
                    cursor: "pointer",
                  }}
                >
                  <td>{t.id}</td>
                  <td>{t.type}</td>
                  <td>
                    {t.host}
                    {t.port && t.port !== 22 ? `:${t.port}` : ""}
                  </td>
                  <td>{t.user}</td>
                  <td>
                    {Object.entries(t.tags ?? {}).map(([k, v]) => (
                      <span className="pill" key={k}>
                        {k}={v}
                      </span>
                    ))}
                  </td>
                  <td className="actions" onClick={(e) => e.stopPropagation()}>
                    <button
                      onClick={() => onPing(t.id)}
                      disabled={busy === t.id}
                    >
                      {busy === t.id ? "..." : "Ping"}
                    </button>
                    <button onClick={() => props.onOpenTerminal(t.id)}>
                      Terminal
                    </button>
                    <button onClick={() => props.onOpenSftp(t.id)}>SFTP</button>
                    <button onClick={() => onDelete(t.id)}>Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
