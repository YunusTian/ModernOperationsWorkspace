import { useEffect, useState } from "react";
import TargetsPage from "./pages/TargetsPage";
import TerminalPage from "./pages/TerminalPage";
import SftpPage from "./pages/SftpPage";
import WorkflowPage from "./pages/WorkflowPage";
import DockerPage from "./pages/DockerPage";
import AIPage from "./pages/AIPage";
import PluginsPage from "./pages/PluginsPage";
import { App as Api } from "./bindings";

type Tab = "targets" | "terminal" | "sftp" | "workflow" | "docker" | "ai" | "plugins";

export default function App() {
  const [tab, setTab] = useState<Tab>("targets");
  const [activeTarget, setActiveTarget] = useState<string>("");
  const [activeType, setActiveType] = useState<string>("");

  // 当 active target 变化时，若类型不匹配已激活的 tab，自动回落到 targets 页。
  useEffect(() => {
    if (!activeTarget) return;
    if ((tab === "terminal" || tab === "sftp") && activeType !== "ssh") {
      setTab("targets");
    }
    if (tab === "docker" && activeType !== "docker") {
      setTab("targets");
    }
  }, [activeTarget, activeType, tab]);

  // 若切换 target 时未从 TargetsPage 传入 type，从后端补齐一次（例如通过 UI 直接输入 target id 的场景）。
  useEffect(() => {
    if (!activeTarget || activeType) return;
    Api.ListTargets()
      .then((ts) => {
        const t = ts.find((x) => x.id === activeTarget);
        if (t) setActiveType(t.type);
      })
      .catch(() => undefined);
  }, [activeTarget, activeType]);

  const isSSH = activeType === "ssh";
  const isDocker = activeType === "docker";
  const sshTitle = activeTarget && !isSSH ? "Active target is not an SSH target" : "";
  const dkTitle = activeTarget && !isDocker ? "Active target is not a Docker target" : "";

  return (
    <div className="layout">
      <aside className="sidebar">
        <h1>MOW</h1>
        <nav>
          <button
            className={tab === "targets" ? "active" : ""}
            onClick={() => setTab("targets")}
          >
            Targets
          </button>
          <button
            className={tab === "terminal" ? "active" : ""}
            onClick={() => setTab("terminal")}
            disabled={!activeTarget || !isSSH}
            title={activeTarget ? sshTitle : "Select an SSH target first"}
          >
            Terminal {activeTarget && isSSH && `· ${activeTarget}`}
          </button>
          <button
            className={tab === "sftp" ? "active" : ""}
            onClick={() => setTab("sftp")}
            disabled={!activeTarget || !isSSH}
            title={activeTarget ? sshTitle : "Select an SSH target first"}
          >
            SFTP {activeTarget && isSSH && `· ${activeTarget}`}
          </button>
          <button
            className={tab === "docker" ? "active" : ""}
            onClick={() => setTab("docker")}
            disabled={!activeTarget || !isDocker}
            title={activeTarget ? dkTitle : "Select a Docker target first"}
          >
            Docker {activeTarget && isDocker && `· ${activeTarget}`}
          </button>
          <button
            className={tab === "workflow" ? "active" : ""}
            onClick={() => setTab("workflow")}
          >
            Workflow {activeTarget && `· ${activeTarget}`}
          </button>
          <button className={tab === "ai" ? "active" : ""} onClick={() => setTab("ai")}>
            AI
          </button>
          <button
            className={tab === "plugins" ? "active" : ""}
            onClick={() => setTab("plugins")}
          >
            Plugins
          </button>
        </nav>
      </aside>
      <main className="main">
        {tab === "targets" && (
          <TargetsPage
            active={activeTarget}
            onPick={(id, type) => {
              setActiveTarget(id);
              setActiveType(type);
            }}
            onOpenTerminal={(id) => {
              setActiveTarget(id);
              setActiveType("ssh");
              setTab("terminal");
            }}
            onOpenSftp={(id) => {
              setActiveTarget(id);
              setActiveType("ssh");
              setTab("sftp");
            }}
            onOpenDocker={(id) => {
              setActiveTarget(id);
              setActiveType("docker");
              setTab("docker");
            }}
          />
        )}
        {tab === "terminal" && activeTarget && isSSH && (
          <TerminalPage targetID={activeTarget} />
        )}
        {tab === "sftp" && activeTarget && isSSH && (
          <SftpPage targetID={activeTarget} />
        )}
        {tab === "docker" && activeTarget && isDocker && (
          <DockerPage targetID={activeTarget} />
        )}
        {tab === "workflow" && <WorkflowPage activeTarget={activeTarget} />}
        {tab === "ai" && <AIPage />}
        {tab === "plugins" && <PluginsPage />}
      </main>
    </div>
  );
}
