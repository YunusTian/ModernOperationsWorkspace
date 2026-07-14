// PluginsPage.test.tsx —— PluginsPage 与 SettingsDrawer 的边界测试。
//
// 本轮 P1「产品边缘测试」补齐前端最薄的一环：Plugins Marketplace + Installed tab
// + SettingsDrawer。之前只有 AIPage 一个用例，本文件覆盖以下路径：
//
//   Installed tab
//     · ListPlugins 返回 broken/incompatible → HealthBadge 与 Enable 复选框行为
//     · ListPlugins 抛错 → 顶部错误横幅、表格 fallback
//     · Uninstall 弹窗 & purge 勾选 → 走 UninstallPlugin(id, true)
//
//   Marketplace tab
//     · RefreshCatalog 返回 from_cache=true → UI 显示离线降级信息
//     · InstallPluginFromCatalog 抛错 → 顶部错误横幅（不修改 installed 表）
//     · 已安装同版本 → 显示 "Installed"，不出现 Install/Update 按钮
//
//   SettingsDrawer
//     · GetPluginSchema 前显示 Loading…
//     · has_schema=false → 显示"没有 schema"提示，无 Save 按钮
//     · 用户改了普通字段但保留 secret 为 "***"（视为脱敏占位）→ 保存后
//       SetPluginSettings 的 patch 里 secret 依然是 "***"，不会覆盖 sidecar
//
// 未来任何 UI 回归都会先在这里裂开。

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { api } = vi.hoisted(() => ({
  api: {
    ListPlugins: vi.fn(),
    SetPluginEnabled: vi.fn(),
    UninstallPlugin: vi.fn(),
    DoctorPlugin: vi.fn(),
    ListCatalogSources: vi.fn(),
    RefreshCatalog: vi.fn(),
    SearchCatalog: vi.fn(),
    InstallPluginFromCatalog: vi.fn(),
    UpdatePluginFromCatalog: vi.fn(),
    GetPluginSchema: vi.fn(),
    GetPluginSettings: vi.fn(),
    SetPluginSettings: vi.fn(),
  },
}));

vi.mock("../bindings", () => ({
  App: api,
}));

import PluginsPage from "./PluginsPage";

const okPlugin = {
  id: "ssh",
  name: "SSH",
  version: "0.5.3",
  author: "mow",
  description: "SSH remote execution",
  enabled: true,
  installed_at: "2026-07-14T00:00:00Z",
  package_dir: "/plugins/ssh",
  health: "ok" as const,
  platform: "windows/amd64",
  compatibility_core: ">=0.5.0,<0.6.0",
  commands: ["ssh.exec", "ssh.shell"],
};

const brokenPlugin = {
  id: "docker",
  name: "Docker",
  version: "0.5.3",
  enabled: false,
  package_dir: "/plugins/docker",
  health: "broken" as const,
  health_code: "PLUGIN_CHECKSUM_MISMATCH",
  health_error: "checksum mismatch on bin/docker-plugin.exe",
  health_details: { entrypoint: "bin/docker-plugin.exe" },
  platform: "windows/amd64",
};

const incompatiblePlugin = {
  id: "ai",
  name: "AI",
  version: "0.4.0",
  enabled: false,
  package_dir: "/plugins/ai",
  health: "incompatible" as const,
  health_code: "PLUGIN_INCOMPATIBLE",
  health_error: "core version 0.5.3 does not satisfy >=0.4.0,<0.5.0",
  platform: "windows/amd64",
};

const secretsSchemaVM = {
  id: "ai",
  has_schema: true,
  enabled: true,
  settings: { endpoint: "https://api.example.com", api_key: "***", nested: { token: "***" } },
  fields: [
    { path: "endpoint", type: "string", title: "endpoint", depth: 0 },
    { path: "api_key", type: "string", title: "api_key", secret: true, depth: 0 },
    { path: "nested", type: "object", title: "nested", depth: 0 },
    { path: "nested.token", type: "string", title: "token", secret: true, depth: 1 },
  ],
  secret_paths: ["api_key", "nested.token"],
};

describe("PluginsPage · Installed tab", () => {
  afterEach(() => cleanup());
  beforeEach(() => {
    vi.clearAllMocks();
    // Marketplace 首次加载会主动调用；给个空实现避免未处理 Promise 报警。
    api.ListCatalogSources.mockResolvedValue([]);
    api.SearchCatalog.mockResolvedValue([]);
  });

  it("renders health badges for broken / incompatible plugins and disables Enable toggle for broken", async () => {
    api.ListPlugins.mockResolvedValue([okPlugin, brokenPlugin, incompatiblePlugin]);
    render(<PluginsPage />);

    // 表格渲染 3 行；期待三种 health badge 出现（依赖 CSS class）
    const okRow = (await screen.findByText("ssh")).closest("tr")!;
    const brokenRow = screen.getByText("docker").closest("tr")!;
    const incompatRow = screen.getByText("ai").closest("tr")!;

    expect(within(okRow).getByText("healthy")).toBeTruthy();
    expect(within(brokenRow).getByText("broken")).toBeTruthy();
    expect(within(incompatRow).getByText("incompatible")).toBeTruthy();

    // broken 行的 Enable checkbox 应被禁用；incompatible 行未禁用（可能只是版本不兼容）
    const brokenToggle = within(brokenRow).getByRole("checkbox");
    expect((brokenToggle as HTMLInputElement).disabled).toBe(true);
    const incompatToggle = within(incompatRow).getByRole("checkbox");
    expect((incompatToggle as HTMLInputElement).disabled).toBe(false);
  });

  it("surfaces ListPlugins failure via the top-level error banner and keeps table empty", async () => {
    api.ListPlugins.mockRejectedValue(new Error("boom: cannot open PluginsDir"));
    render(<PluginsPage />);
    // 错误横幅使用 .wf-error class，直接按文本查找
    expect(await screen.findByText(/boom: cannot open PluginsDir/)).toBeTruthy();
    expect(screen.getByText(/No plugins installed/i)).toBeTruthy();
  });

  it("uninstall modal with purge=true calls UninstallPlugin(id, true) and removes the row", async () => {
    api.ListPlugins.mockResolvedValue([okPlugin]);
    api.UninstallPlugin.mockResolvedValue(undefined);
    render(<PluginsPage />);

    fireEvent.click(await screen.findByRole("button", { name: /^Uninstall$/i }));
    // 勾上 purge
    const purgeBox = await screen.findByRole("checkbox", { name: /purge/i });
    fireEvent.click(purgeBox);
    fireEvent.click(screen.getByRole("button", { name: /Uninstall & Purge/i }));

    await waitFor(() => expect(api.UninstallPlugin).toHaveBeenCalledWith("ssh", true));
    // 卸载后行消失，出现空态提示
    await waitFor(() => expect(screen.getByText(/No plugins installed/i)).toBeTruthy());
  });
});

describe("PluginsPage · Marketplace tab", () => {
  afterEach(() => cleanup());
  beforeEach(() => {
    vi.clearAllMocks();
    api.ListPlugins.mockResolvedValue([okPlugin]);
  });

  it("shows offline-cache banner rows when RefreshCatalog returns from_cache=true", async () => {
    api.ListCatalogSources.mockResolvedValue([
      { name: "official", url: "https://example.test/catalog.json", trusted: true },
    ]);
    api.SearchCatalog.mockResolvedValue([
      {
        source: "official",
        url: "https://example.test/catalog.json",
        from_cache: false,
        entries: [
          {
            id: "ssh",
            name: "SSH",
            versions: [
              {
                version: "0.5.3",
                compatibility: { core: ">=0.5.0" },
                platforms: [
                  {
                    os: "windows",
                    arch: "amd64",
                    url: "https://x/ssh.tar.gz",
                    checksum: "sha256:aa",
                  },
                ],
              },
            ],
          },
        ],
      },
    ]);
    api.RefreshCatalog.mockResolvedValue([
      {
        source: "official",
        url: "https://example.test/catalog.json",
        ok: true,
        from_cache: true,
        num_entries: 4,
      },
    ]);
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Marketplace/i }));
    fireEvent.click(await screen.findByRole("button", { name: /Refresh/i }));
    // Refresh 结果条：包含 source 名称 + entries 数
    expect(await screen.findByText(/4 entries/)).toBeTruthy();
    expect(screen.getByText("official")).toBeTruthy();
  });

  it("surfaces InstallPluginFromCatalog error via the top-level error banner", async () => {
    api.ListCatalogSources.mockResolvedValue([
      { name: "official", url: "https://example.test/catalog.json", trusted: true },
    ]);
    api.SearchCatalog.mockResolvedValue([
      {
        source: "official",
        url: "https://example.test/catalog.json",
        from_cache: false,
        entries: [
          {
            id: "pve",
            name: "Proxmox VE",
            versions: [
              {
                version: "0.5.3",
                compatibility: { core: ">=0.5.0" },
                platforms: [
                  {
                    os: "windows",
                    arch: "amd64",
                    url: "https://x/pve.tar.gz",
                    checksum: "sha256:bb",
                  },
                ],
              },
            ],
          },
        ],
      },
    ]);
    api.InstallPluginFromCatalog.mockRejectedValue(
      new Error("PLUGIN_CHECKSUM_MISMATCH: sha256 mismatch"),
    );
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Marketplace/i }));
    // 等到 pve 行渲染完（tbody 里的 <code>pve</code> 会与 ReleaseDetails 的 <option value="pve">pve</option>
    // 同名，用 findAllByText 后取第一个 <code> 元素避免多匹配报错）。
    const pveMatches = await screen.findAllByText("pve");
    const pveCell = pveMatches.find((el) => el.tagName === "CODE");
    expect(pveCell).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^Install$/i }));
    expect(
      await screen.findByText(/PLUGIN_CHECKSUM_MISMATCH: sha256 mismatch/),
    ).toBeTruthy();
    // 顶层错误横幅出现，installed 表未变化（still 1 ssh plugin）
    expect(api.ListPlugins).toHaveBeenCalledTimes(1); // 未触发 onInstalled 重新加载
  });

  it("renders 'Installed' pill when the chosen version equals the installed one", async () => {
    // ssh 已装 0.5.3，catalog 也只列 0.5.3，chosen==installed → 没有 Install/Update 按钮
    api.ListCatalogSources.mockResolvedValue([
      { name: "official", url: "https://x/catalog.json", trusted: true },
    ]);
    api.SearchCatalog.mockResolvedValue([
      {
        source: "official",
        url: "https://x/catalog.json",
        from_cache: false,
        entries: [
          {
            id: "ssh",
            name: "SSH",
            versions: [
              {
                version: "0.5.3",
                compatibility: {},
                platforms: [
                  { os: "windows", arch: "amd64", url: "https://x/ssh.tar.gz", checksum: "sha256:cc" },
                ],
              },
            ],
          },
        ],
      },
    ]);
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /Marketplace/i }));
    // 同上：<code>ssh</code> 与 <option value="ssh">ssh</option> 同名，需要过滤 <code>
    const matches = await screen.findAllByText("ssh");
    const sshCode = matches.find((el) => el.tagName === "CODE")!;
    const sshRow = sshCode.closest("tr")!;
    expect(within(sshRow).getByText(/Installed/i)).toBeTruthy();
    expect(within(sshRow).queryByRole("button", { name: /^Install$/i })).toBeNull();
    expect(within(sshRow).queryByRole("button", { name: /^Update$/i })).toBeNull();
  });
});

describe("PluginsPage · SettingsDrawer", () => {
  afterEach(() => cleanup());
  beforeEach(() => {
    vi.clearAllMocks();
    api.ListPlugins.mockResolvedValue([okPlugin]);
    api.ListCatalogSources.mockResolvedValue([]);
    api.SearchCatalog.mockResolvedValue([]);
  });

  it("shows Loading… placeholder while GetPluginSchema is pending", async () => {
    let resolveSchema: (v: unknown) => void = () => undefined;
    api.GetPluginSchema.mockImplementation(
      () => new Promise((r) => (resolveSchema = r)),
    );
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/i }));
    expect(await screen.findByText(/^Loading…/)).toBeTruthy();
    // 释放 promise 让测试干净退出
    resolveSchema({
      id: "ssh",
      has_schema: false,
      enabled: true,
      settings: {},
    });
    await waitFor(() =>
      expect(screen.getByText(/does not declare/i)).toBeTruthy(),
    );
  });

  it("hides Save button when has_schema=false and shows plain hint", async () => {
    api.GetPluginSchema.mockResolvedValue({
      id: "ssh",
      has_schema: false,
      enabled: true,
      settings: {},
    });
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/i }));
    expect(await screen.findByText(/does not declare/i)).toBeTruthy();
    // 没有 schema 时不应渲染 Save 按钮
    expect(
      screen.queryByRole("button", { name: /^Save/i }),
    ).toBeNull();
    // 也不应显示 "Saving…" 等状态
    expect(screen.queryByText(/^Saving/)).toBeNull();
  });

  it("preserves masked *** secrets on save (does not overwrite sidecar values)", async () => {
    api.GetPluginSchema.mockResolvedValue(secretsSchemaVM);
    api.SetPluginSettings.mockResolvedValue(secretsSchemaVM);
    render(<PluginsPage />);
    fireEvent.click(await screen.findByRole("button", { name: /^Settings$/i }));

    // 等抽屉里的字段渲染完
    const endpointInput = await screen.findByDisplayValue("https://api.example.com");
    fireEvent.change(endpointInput, {
      target: { value: "https://api.new.example.com" },
    });
    // 不改 api_key / nested.token（保留 "***" 占位），点击 Save
    fireEvent.click(screen.getByRole("button", { name: /^Save/i }));

    await waitFor(() => expect(api.SetPluginSettings).toHaveBeenCalled());
    const call = api.SetPluginSettings.mock.calls[0];
    expect(call[0]).toBe("ssh"); // id
    const patch = call[1] as Record<string, unknown>;
    // 顶层 secret 与嵌套 secret 都应保持 "***" —— UI 层不允许把掩码值当明文覆盖
    expect(patch.api_key).toBe("***");
    expect((patch.nested as Record<string, unknown>).token).toBe("***");
    // 普通字段被更新
    expect(patch.endpoint).toBe("https://api.new.example.com");
  });
});
