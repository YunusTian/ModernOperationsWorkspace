import { useEffect, useState } from "react";
import {
  App,
  TargetVM,
  UpsertDockerTargetInput,
  UpsertSSHTargetInput,
} from "../bindings";

type Props = {
  active: string;
  onPick: (id: string, type: string) => void;
  onOpenTerminal: (id: string) => void;
  onOpenSftp: (id: string) => void;
  onOpenDocker: (id: string) => void;
};

type FormMode = "closed" | "ssh" | "docker";

const emptySSH: UpsertSSHTargetInput = {
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

const emptyDocker: UpsertDockerTargetInput = {
  id: "",
  name: "",
  host: "unix:///var/run/docker.sock",
  api_version: "",
  tls_verify: false,
  tls_ca: "",
  tls_cert: "",
  tls_key: "",
};

export default function TargetsPage(props: Props) {
  const [targets, setTargets] = useState<TargetVM[]>([]);
  const [err, setErr] = useState<string>("");
  const [busy, setBusy] = useState<string>("");
  const [mode, setMode] = useState<FormMode>("closed");
  const [sshForm, setSshForm] = useState<UpsertSSHTargetInput>(emptySSH);
  const [dockerForm, setDockerForm] =
    useState<UpsertDockerTargetInput>(emptyDocker);

  const refresh = () => {
    setErr("");
    App.ListTargets()
      .then(setTargets)
      .catch((e) => setErr(String(e)));
  };
  useEffect(refresh, []);

  const onSubmitSSH = async () => {
    if (!sshForm.id || !sshForm.host || !sshForm.user) {
      setErr("id / host / user 必填");
      return;
    }
    try {
      await App.UpsertSSHTarget(sshForm);
      setMode("closed");
      setSshForm(emptySSH);
      refresh();
    } catch (e) {
      setErr(String(e));
    }
  };

  const onSubmitDocker = async () => {
    if (!dockerForm.id || !dockerForm.host) {
      setErr("id / host 必填");
      return;
    }
    // v0.3 硬护栏：Windows named pipe 尚未实现。
    // 后端 core/connection.DockerCredentials.Validate 出于向后兼容仍允许该
    // scheme，但运行时会返回 DOCKER_NPIPE_UNSUPPORTED（见 plugins/docker/client.go）。
    // UI 层在保存前直接拒绝，避免用户配置一个永远拨不通的 target。
    // 计划在 v0.3.1 补齐 npipe 支持，届时移除本判断。
    if (dockerForm.host.trim().toLowerCase().startsWith("npipe://")) {
      setErr(
        "npipe:// 目标暂不支持（计划 v0.3.1 补齐）。请使用 unix:// (Linux/macOS) 或 tcp://[+TLS] (远端)。",
      );
      return;
    }
    try {
      await App.UpsertDockerTarget(dockerForm);
      setMode("closed");
      setDockerForm(emptyDocker);
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
        <button
          onClick={() => setMode(mode === "ssh" ? "closed" : "ssh")}
          className={mode === "ssh" ? "" : ""}
        >
          {mode === "ssh" ? "Cancel" : "+ Add SSH Target"}
        </button>
        <button
          onClick={() => setMode(mode === "docker" ? "closed" : "docker")}
        >
          {mode === "docker" ? "Cancel" : "+ Add Docker Target"}
        </button>
        <button className="secondary" onClick={refresh}>
          Refresh
        </button>
        <span style={{ flex: 1 }} />
        {err && <span className="error">{err}</span>}
      </div>

      {mode === "ssh" && (
        <div className="form">
          <label>
            ID
            <input
              value={sshForm.id}
              onChange={(e) => setSshForm({ ...sshForm, id: e.target.value })}
              placeholder="e.g. srv-web-01"
            />
          </label>
          <label>
            Name
            <input
              value={sshForm.name ?? ""}
              onChange={(e) => setSshForm({ ...sshForm, name: e.target.value })}
            />
          </label>
          <label>
            Host
            <input
              value={sshForm.host}
              onChange={(e) => setSshForm({ ...sshForm, host: e.target.value })}
            />
          </label>
          <label>
            Port
            <input
              type="number"
              value={sshForm.port ?? 22}
              onChange={(e) =>
                setSshForm({ ...sshForm, port: Number(e.target.value) })
              }
            />
          </label>
          <label>
            User
            <input
              value={sshForm.user}
              onChange={(e) => setSshForm({ ...sshForm, user: e.target.value })}
            />
          </label>
          <label>
            Auth Method
            <select
              value={sshForm.method}
              onChange={(e) =>
                setSshForm({
                  ...sshForm,
                  method: e.target.value as UpsertSSHTargetInput["method"],
                })
              }
            >
              <option value="password">password</option>
              <option value="privatekey">privatekey</option>
              <option value="agent">agent</option>
            </select>
          </label>
          {sshForm.method === "password" && (
            <label>
              Password
              <input
                type="password"
                value={sshForm.password ?? ""}
                onChange={(e) =>
                  setSshForm({ ...sshForm, password: e.target.value })
                }
              />
            </label>
          )}
          {sshForm.method === "privatekey" && (
            <>
              <label style={{ gridColumn: "1/-1" }}>
                Private Key (PEM)
                <textarea
                  rows={4}
                  value={sshForm.private_key ?? ""}
                  onChange={(e) =>
                    setSshForm({ ...sshForm, private_key: e.target.value })
                  }
                />
              </label>
              <label>
                Passphrase (optional)
                <input
                  type="password"
                  value={sshForm.passphrase ?? ""}
                  onChange={(e) =>
                    setSshForm({ ...sshForm, passphrase: e.target.value })
                  }
                />
              </label>
            </>
          )}
          <label>
            known_hosts mode
            <select
              value={sshForm.known_hosts_mode ?? "accept-new"}
              onChange={(e) =>
                setSshForm({
                  ...sshForm,
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
            <button onClick={onSubmitSSH}>Save</button>
            <button
              className="secondary"
              onClick={() => {
                setMode("closed");
                setSshForm(emptySSH);
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      {mode === "docker" && (
        <div className="form">
          <label>
            ID
            <input
              value={dockerForm.id}
              onChange={(e) =>
                setDockerForm({ ...dockerForm, id: e.target.value })
              }
              placeholder="e.g. dk-prod-01"
            />
          </label>
          <label>
            Name
            <input
              value={dockerForm.name ?? ""}
              onChange={(e) =>
                setDockerForm({ ...dockerForm, name: e.target.value })
              }
            />
          </label>
          <label style={{ gridColumn: "1/-1" }}>
            Docker Host
            <input
              value={dockerForm.host}
              onChange={(e) =>
                setDockerForm({ ...dockerForm, host: e.target.value })
              }
              placeholder="unix:///var/run/docker.sock  |  tcp://10.0.0.5:2376"
            />
            {dockerForm.host.trim().toLowerCase().startsWith("npipe://") && (
              <small style={{ color: "#e5c07b" }}>
                v0.3 暂不支持 npipe:// —— 请改用 unix:// 或 tcp://；v0.3.1 补齐 Windows named pipe。
              </small>
            )}
          </label>
          <label>
            API Version (optional)
            <input
              value={dockerForm.api_version ?? ""}
              onChange={(e) =>
                setDockerForm({ ...dockerForm, api_version: e.target.value })
              }
              placeholder="e.g. 1.44"
            />
          </label>
          <label>
            TLS Verify
            <select
              value={dockerForm.tls_verify ? "true" : "false"}
              onChange={(e) =>
                setDockerForm({
                  ...dockerForm,
                  tls_verify: e.target.value === "true",
                })
              }
            >
              <option value="false">off</option>
              <option value="true">on</option>
            </select>
          </label>
          {(dockerForm.tls_verify || dockerForm.tls_ca) && (
            <>
              <label style={{ gridColumn: "1/-1" }}>
                CA (PEM)
                <textarea
                  rows={3}
                  value={dockerForm.tls_ca ?? ""}
                  onChange={(e) =>
                    setDockerForm({ ...dockerForm, tls_ca: e.target.value })
                  }
                />
              </label>
              <label style={{ gridColumn: "1/-1" }}>
                Client Cert (PEM)
                <textarea
                  rows={3}
                  value={dockerForm.tls_cert ?? ""}
                  onChange={(e) =>
                    setDockerForm({ ...dockerForm, tls_cert: e.target.value })
                  }
                />
              </label>
              <label style={{ gridColumn: "1/-1" }}>
                Client Key (PEM)
                <textarea
                  rows={3}
                  value={dockerForm.tls_key ?? ""}
                  onChange={(e) =>
                    setDockerForm({ ...dockerForm, tls_key: e.target.value })
                  }
                />
              </label>
            </>
          )}
          <div className="form-actions">
            <button onClick={onSubmitDocker}>Save</button>
            <button
              className="secondary"
              onClick={() => {
                setMode("closed");
                setDockerForm(emptyDocker);
              }}
            >
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="content">
        {targets.length === 0 ? (
          <p style={{ color: "#888" }}>
            暂无 Target。点击 "+ Add SSH Target" 或 "+ Add Docker Target" 新建。
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Type</th>
                <th>Endpoint</th>
                <th>Tags</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {targets.map((t) => (
                <tr
                  key={t.id}
                  onClick={() => props.onPick(t.id, t.type)}
                  style={{
                    background: t.id === props.active ? "#094771" : undefined,
                    cursor: "pointer",
                  }}
                >
                  <td>{t.id}</td>
                  <td>
                    <span className={`dk-type dk-type-${t.type}`}>{t.type}</span>
                  </td>
                  <td>{t.display_host || `${t.host}${t.port ? ":" + t.port : ""}`}</td>
                  <td>
                    {Object.entries(t.tags ?? {}).map(([k, v]) => (
                      <span className="pill" key={k}>
                        {k}={v}
                      </span>
                    ))}
                  </td>
                  <td className="actions" onClick={(e) => e.stopPropagation()}>
                    {t.type === "ssh" && (
                      <>
                        <button
                          onClick={() => onPing(t.id)}
                          disabled={busy === t.id}
                        >
                          {busy === t.id ? "..." : "Ping"}
                        </button>
                        <button onClick={() => props.onOpenTerminal(t.id)}>
                          Terminal
                        </button>
                        <button onClick={() => props.onOpenSftp(t.id)}>
                          SFTP
                        </button>
                      </>
                    )}
                    {t.type === "docker" && (
                      <button onClick={() => props.onOpenDocker(t.id)}>
                        Dashboard
                      </button>
                    )}
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
