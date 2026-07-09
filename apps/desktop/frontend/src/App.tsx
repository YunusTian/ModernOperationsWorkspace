import { useState } from "react";
import TargetsPage from "./pages/TargetsPage";
import TerminalPage from "./pages/TerminalPage";
import SftpPage from "./pages/SftpPage";

type Tab = "targets" | "terminal" | "sftp";

export default function App() {
  const [tab, setTab] = useState<Tab>("targets");
  const [activeTarget, setActiveTarget] = useState<string>("");

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
            disabled={!activeTarget}
            title={activeTarget ? "" : "Select a target first"}
          >
            Terminal {activeTarget && `· ${activeTarget}`}
          </button>
          <button
            className={tab === "sftp" ? "active" : ""}
            onClick={() => setTab("sftp")}
            disabled={!activeTarget}
            title={activeTarget ? "" : "Select a target first"}
          >
            SFTP {activeTarget && `· ${activeTarget}`}
          </button>
        </nav>
      </aside>
      <main className="main">
        {tab === "targets" && (
          <TargetsPage
            active={activeTarget}
            onPick={(id) => setActiveTarget(id)}
            onOpenTerminal={(id) => {
              setActiveTarget(id);
              setTab("terminal");
            }}
            onOpenSftp={(id) => {
              setActiveTarget(id);
              setTab("sftp");
            }}
          />
        )}
        {tab === "terminal" && activeTarget && (
          <TerminalPage targetID={activeTarget} />
        )}
        {tab === "sftp" && activeTarget && <SftpPage targetID={activeTarget} />}
      </main>
    </div>
  );
}
