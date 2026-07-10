// bindings.ts —— 后端 App 方法的类型化包装。
//
// Wails 会在 `wails dev` 时自动生成 wailsjs/go/main/App 中的绑定；
// 为了让前端在没跑 wails 时也能通过 TypeScript 类型检查（例如仅 vite build 时），
// 这里做一层薄封装：
//   - 若存在 wailsjs 生成的模块，直接透传；
//   - 若不存在（例如首次 vite build 前），退化到 window.go.main.App 的运行时查找。
//
// 只导出前端需要用到的方法签名。

export type TargetVM = {
  id: string;
  type: string;
  name: string;
  host: string;
  port: number;
  user: string;
  tags: Record<string, string>;
  created_at: string;
  updated_at: string;
};

export type UpsertSSHTargetInput = {
  id: string;
  name?: string;
  host: string;
  port?: number;
  user: string;
  tags?: Record<string, string>;
  method: "password" | "privatekey" | "agent";
  password?: string;
  private_key?: string;
  passphrase?: string;
  known_hosts_mode?: "strict" | "accept-new" | "insecure-ignore";
  known_hosts_path?: string;
};

export type SFTPEntry = {
  name: string;
  size: number;
  mode: string;
  mod_time: string;
  is_dir: boolean;
  is_link: boolean;
};

export type SFTPListResult = {
  path: string;
  entries: SFTPEntry[];
};

export type ShellOpenInput = {
  term?: string;
  rows?: number;
  cols?: number;
};

// -----------------------------------------------------------------------------
// Workflow
// -----------------------------------------------------------------------------

export type WorkflowInputVM = {
  name: string;
  type: string;
  required: boolean;
  default?: unknown;
  description?: string;
};

export type WorkflowValidateResult = {
  ok: boolean;
  id: string;
  name: string;
  description: string;
  step_count: number;
  inputs: WorkflowInputVM[];
};

export type WorkflowRunInput = {
  session_id: string;
  yaml: string;
  target: string;
  inputs: Record<string, unknown>;
};

export type WorkflowStepEvent = {
  phase: "start" | "finish" | "error";
  index: number;
  step_id: string;
  kind: "cmd" | "recipe";
  ref: string;
  duration_ms?: number;
  error_code?: string;
  error_msg?: string;
};

export type WorkflowDoneEvent = {
  ok: boolean;
  duration_ms: number;
  error?: string;
};

// 通过 wails 运行时 (window.go.main.App) 调用方法；
// 若绑定不存在（如 vite dev 未连上 wails），返回一个明确错误便于排查。
function call<T = unknown>(name: string, ...args: unknown[]): Promise<T> {
  const w = window as unknown as {
    go?: { main?: { App?: Record<string, (...a: unknown[]) => Promise<T>> } };
  };
  const fn = w.go?.main?.App?.[name];
  if (!fn) {
    return Promise.reject(
      new Error(
        `wails binding go.main.App.${name} not available (are you running via 'wails dev'?)`,
      ),
    );
  }
  return fn(...args);
}

export const App = {
  ListTargets: () => call<TargetVM[]>("ListTargets"),
  UpsertSSHTarget: (in_: UpsertSSHTargetInput) =>
    call<void>("UpsertSSHTarget", in_),
  DeleteTarget: (id: string) => call<void>("DeleteTarget", id),
  PingTarget: (id: string) => call<string>("PingTarget", id),

  SftpList: (targetID: string, remotePath: string) =>
    call<SFTPListResult>("SftpList", targetID, remotePath),
  SftpUpload: (targetID: string, localPath: string, remotePath: string) =>
    call<void>("SftpUpload", targetID, localPath, remotePath),
  SftpDownload: (targetID: string, remotePath: string, localPath: string) =>
    call<void>("SftpDownload", targetID, remotePath, localPath),

  ShellOpen: (targetID: string, in_: ShellOpenInput) =>
    call<string>("ShellOpen", targetID, in_),
  ShellWrite: (sessionID: string, dataB64: string) =>
    call<void>("ShellWrite", sessionID, dataB64),
  ShellResize: (sessionID: string, rows: number, cols: number) =>
    call<void>("ShellResize", sessionID, rows, cols),
  ShellClose: (sessionID: string) => call<void>("ShellClose", sessionID),

  WorkflowValidate: (yamlText: string) =>
    call<WorkflowValidateResult>("WorkflowValidate", yamlText),
  WorkflowRun: (in_: WorkflowRunInput) => call<void>("WorkflowRun", in_),
};

// -----------------------------------------------------------------------------
// Wails Events 桥接（EventsOn / EventsOff）
// -----------------------------------------------------------------------------

type WailsEventsGlobal = {
  EventsOn?: (
    name: string,
    cb: (...data: unknown[]) => void,
  ) => (() => void) | void;
  EventsOff?: (name: string, ...more: string[]) => void;
};

function eventsGlobal(): WailsEventsGlobal | undefined {
  const w = window as unknown as {
    runtime?: WailsEventsGlobal;
    wails?: WailsEventsGlobal;
  };
  return w.runtime ?? w.wails;
}

export function eventsOn(name: string, cb: (...data: unknown[]) => void) {
  const g = eventsGlobal();
  if (!g?.EventsOn) {
    console.warn(`wails runtime not available; skipping EventsOn(${name})`);
    return () => undefined;
  }
  const off = g.EventsOn(name, cb);
  return () => {
    if (typeof off === "function") off();
    else g.EventsOff?.(name);
  };
}
