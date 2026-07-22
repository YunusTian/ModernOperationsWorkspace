# Changelog

所有对本项目的重要变更都将记录在此文件中。

格式基于 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [v0.5.4] - 2026-07-22

### v0.5.4 开发者体验（DX）—— `plugin init` / `plugin lint` 测试 + `plugin dev --watch` 热重载 + Authoring Guide

- **背景**：v0.5.0–v0.5.3 已把插件平台的运行时地基做完（Manifest / 生命周期 / Catalog / Schema-driven UI / PVE 参考实现），但外部开发者上手成本依旧偏高——[apps/cli/plugin_dev.go](./apps/cli/plugin_dev.go) 的 `plugin init` / `plugin lint` / `plugin package` 三条 DX 命令虽已落地，却**没有单测**，也**没有面向第三方作者的连贯文档**；日常迭代还得 `package → install --path → enable` 三步走。本轮补齐测试、文档，并新增 `mow plugin dev [--watch]` 把日常迭代压到一步。不改动 SDK / Manifest / Plugin Protocol。
- **新增命令 `mow plugin dev`**（[apps/cli/plugin_watch.go](./apps/cli/plugin_watch.go)）：
    - 数据流：`stageDevPackage`（复用 §6 的 build + platforms 裁剪 + checksum 注入）→ 通过 `core/plugin.Lifecycle` 走 `Install`（首次）或 `Update`（后续，原子替换 + 失败回退）到 `<PluginsDir>/<id>` → 自动 `SetEnabled(true)`，改完就能 `mow run <id>.<cmd>`
    - `--watch`：轮询 `<srcDir>` 下的 `*.go / *.yaml / *.yml / *.json / go.mod / go.sum` 的 mtime，跳过 `.git / vendor / dist / node_modules`；`--interval` 控制间隔（默认 500ms）。**不引 fsnotify** —— 一个 dev 工具没必要拉跨平台原生依赖，轮询对本地开发中小型仓库完全够用，并规避 Windows fsnotify 的 rename+create 顺序、隐藏文件等边界
    - 强制 host GOOS/GOARCH：Manifest 的 `platforms[]` 会裁剪成当前平台条目，跨平台 dev 无意义（参数校验阶段直接拒绝）
    - build 失败**不退出 watch loop**，允许用户改回来继续；`stderr` 打印错误但循环继续
    - **依赖注入 `packageBuilder`**：生产实现 `goBuilder{}` 走 `runGoBuild`，测试实现只写占位字节；单测因此无需 `go` 工具链
- **`plugin_dev.go` 命令注册**：[apps/cli/plugin.go](./apps/cli/plugin.go) 的 `newPluginCmd` 追加 `newPluginDevCmd(holder)`，与 `init / lint / package / validate / list / install / update / uninstall / enable / disable / doctor / search / catalog / config` 并列
- **新增测试（10 个用例）**：
    - [apps/cli/plugin_dev_test.go](./apps/cli/plugin_dev_test.go) 共 6 个用例（init 脚手架完整性、非法 ID 拦截、`--force` 覆盖语义、lint 主路径 + `--json` schema、`PLUGIN_MANIFEST_INVALID` 错误码传播），全部不依赖 `go` 工具链
    - [apps/cli/plugin_watch_test.go](./apps/cli/plugin_watch_test.go) 新增 4 个用例：
        - `TestPluginDev_FirstRunInstallsAndEnables`：首次运行走 `Install`，state 里 `enabled=true`，`bin/mow-<id>-plugin(.exe)` 与 `plugin.json` 均落到 `<PluginsDir>/<id>`
        - `TestPluginDev_SecondRunUpdates`：mutate fake 二进制内容让 checksum 变化 → 走 `Update` 路径，stdout 报告 `updated`
        - `TestPluginDev_WatchTriggersRebuildOnMtimeBump`：`--watch --interval=20ms --MaxCycles=1`，测试线程 `Chtimes(main.go, +2s)` bump mtime，watch loop 触发第二次 build 后优雅退出
        - `TestPluginDev_RejectsCrossPlatform`：`GOOS != runtime.GOOS` 直接拒绝
- **命名冲突处理**：[apps/cli/plugin_catalog_test.go](./apps/cli/plugin_catalog_test.go) 已定义 `runCLI(t, cfgPath, args...)`；本轮新增的 test file 改用 `runPluginDevCLI(t, args...)` 独立命名，避免同包重复声明
- **新增文档**：[docs/plugin-authoring.md](./docs/plugin-authoring.md) 10 节开发者旅程：
    - §2 三分钟脚手架 · §3 `plugin lint` vs `plugin validate` 差异矩阵 · §4 权限语义 / 稳定错误码 · §5 SDK Conformance · §6 `plugin package` · **§7.1 `plugin dev --watch` 热重载**（含数据流 / 白名单 / 典型 dev 循环 shell 输出） · §8 Catalog 发布 · §9 迭代小抄 · §10 已知限制
- **不涉及**：
    - 不改 SDK / Plugin Protocol / Manifest schema
    - `plugin package` 测试仍未纳入常规 CI（需真实 `go build`；本轮 dev 测试用假 builder 规避了同类问题）
    - 不改动 CI / Release workflow
- **本地验证**：
    - `go test ./apps/cli/ -run "TestPluginInit|TestPluginLint|TestPluginDev" -count=1 -v` → 10/10 PASS
    - `go test ./apps/cli/ -count=1` → PASS（4.705s，全量 CLI 测试）
    - `go vet ./apps/cli/...` → clean

### 前端边缘测试补齐（PluginsPage + SettingsDrawer）

- **背景**：v0.5.2 落地 Plugins Marketplace + Schema-driven SettingsDrawer 时，前端只有 [AIPage.test.tsx](./apps/desktop/frontend/src/pages/AIPage.test.tsx) 一个用例；`***` 掩码回传、离线降级、install 失败横幅等 UI 边界只靠人工验收。
- **新增**：[apps/desktop/frontend/src/pages/PluginsPage.test.tsx](./apps/desktop/frontend/src/pages/PluginsPage.test.tsx)，共 9 个用例，纯 vitest + `@testing-library/react`（jsdom），不依赖 Wails 运行时（`../bindings` 走 `vi.mock`）：
    - Installed tab：`healthy / incompatible / broken` HealthBadge 渲染；`broken` 插件的 Enable checkbox 禁用；`ListPlugins` 抛错 → 顶层错误横幅 + 表格空态提示；Uninstall 弹窗勾选 purge → `UninstallPlugin(id, true)` + 行从表格中消失
    - Marketplace tab：`RefreshCatalog` 返回 `from_cache=true` → "N entries" 提示行；`InstallPluginFromCatalog` 抛错 → 顶层错误横幅、不重新调用 `ListPlugins`；已安装同版本 → `Installed` pill、不出现 Install / Update 按钮
    - SettingsDrawer：`GetPluginSchema` pending → `Loading…` 占位；`has_schema=false` → 提示文案 + 不渲染 Save 按钮；**secret 保留语义**（回归主用例）—— 用户仅改普通字段、`api_key` 与 `nested.token` 保留 `"***"` 占位 → `SetPluginSettings` payload 里 secret 仍为 `"***"`，不会覆盖 sidecar 明文
- **CI 覆盖**：[.github/workflows/ci.yml](./.github/workflows/ci.yml#L131) 的 `frontend` job 已跑 `npm test`（vitest run）+ `npm run build`（tsc + vite），本文件自动纳入门禁；本地 `npm run test` 14/14 通过；`npx tsc -b` 通过。
- **不涉及**：Go / Wails 后端；SDK / Plugin Protocol / Manifest schema；已发布产物；本轮不改动 v0.5.3 tag 的范围。

## [v0.5.3] - 2026-07-14

### Release Smoke Patch（Windows catalog 平台过滤）

v0.5.2 的 patch 版本，**不引入任何新特性**；SDK / Manifest / Plugin Protocol 完全不变，已发布的 v0.5.2 catalog 与二进制无需重打。[VERSION](./VERSION) 由 `0.5.2` 上调为 `0.5.3`。

- **修复**：Windows install-smoke 只下载当前 target/arch 的 tar.gz，历史 [release-smoke.ps1](./scripts/release-smoke.ps1) 在 catalog Phase 2 对 `entries[].versions[].platforms[]` 里其它平台的条目仍调用 `Resolve-Path`，遇到磁盘上不存在的产物直接抛错，导致 Windows Phase 2 catalog install smoke 从未成功过。Bash 版本用 `os.path.abspath` 侥幸通过，两端语义不一致。
- **实现**：新增 [scripts/release-smoke-lib.ps1](./scripts/release-smoke-lib.ps1)，导出 `ConvertTo-LocalCatalog -Catalog -ArtifactDir -Target -Arch`：按 target/arch 过滤 `platforms[]`，磁盘上缺失的产物跳过，匹配到的条目才改写为 `file:///` URL；对齐 [release-smoke.sh](./scripts/release-smoke.sh) 的宽松语义。[release-smoke.ps1](./scripts/release-smoke.ps1) 在 Phase 2 改为调用该函数，其余流程保持不变。
- **回归**：新增 [scripts/release-smoke-lib.tests.ps1](./scripts/release-smoke-lib.tests.ps1)，不依赖 Pester，覆盖两种场景：
    - S1（回归主用例）：catalog 含 linux/windows/darwin 三平台，本地目录只有 windows/amd64 产物 → 只保留 windows/amd64、URL 改为 `file:///`、checksum 原样保留。
    - S2（防御用例）：当前平台产物缺失 → `platforms[]` 被清空而不抛出 `Resolve-Path` 错误。
- **CI**：[ci.yml](./.github/workflows/ci.yml) 的 `test (windows-latest)` job 追加 `PowerShell smoke lib regression` 步骤，把上述回归纳入主干门禁，作为打 `v0.5.3` tag 前的兜底检查。
- **文档**：新增 [v0.5.3 验收清单](./docs/v0.5.3-acceptance-checklist.md)；[development-plan-v0.5-v1.0.md §4.5](./docs/development-plan-v0.5-v1.0.md#45-v053release-smoke-patchwindows-catalog-平台过滤) 把原 §4.5「插件开发者体验」下移到 [§4.5.1 v0.5.4](./docs/development-plan-v0.5-v1.0.md#451-v054插件开发者体验v053-后追加可延后到-v06-前)；[roadmap.md v0.5.3](./docs/roadmap.md#v053--release-smoke-patchwindows-catalog-平台过滤-已实现三平台-release-smoke-待-v053-tag-触发) 记录本次修复；[v0.5.2 验收清单](./docs/v0.5.2-acceptance-checklist.md) 附加 patch 引用（不改动其范围与勾选状态）。

## [v0.5.2] - 2026-07-13

> 说明：v0.5.1 没有单独打 tag —— 其"生命周期链路 + 本地 Catalog + Desktop Marketplace + Release Catalog"全部随 `v0.5.2` 一并发布。下方 §v0.5.2 P1、§v0.5.1 P0–P3、§v0.5.1 P1 三个小节共同构成 `v0.5.2` tag 的实际交付内容。

### v0.5.2 P1 — Schema 驱动的配置 UI + Secret sidecar + PVE 参考插件

- **文档收尾**：新增 [v0.5.2 验收清单](./docs/v0.5.2-acceptance-checklist.md)（10 节，覆盖 Schema 编译器 / SecretStore / CLI / Desktop / PVE / CI+Release / 数据生命周期 / 安全承诺 / 发布门槛 / 延后项）；[plugin-system.md](./docs/plugin-system.md) 新增 §9「数据与凭据生命周期（v0.5.2）」，含目录布局、生命周期矩阵（install/enable/set/update/uninstall/purge 五列）、权限与原子性、Secret 卫生承诺、手动运维参考；[roadmap.md v0.5.2](./docs/roadmap.md) 从 🚧 改为 ✅；[README.md](./README.md) roadmap 表同步；[development-plan-v0.5-v1.0.md §4.4.1](./docs/development-plan-v0.5-v1.0.md#441-v052-发布门槛) 六条门槛全部勾选。
- **Manifest `settingsSchema` 编译器**：新增 [core/plugin/settings](./core/plugin/settings/schema.go)，覆盖 JSON Schema 子集 + 自定义 `secret` 关键字（`Compile / Validate / ApplyDefaults / Redact / Fields / SetPath / GetPath / SecretPaths`）；不引入重量级依赖。
- **Secret 隔离存储**：[secret_store.go](./core/plugin/settings/secret_store.go) 新增 `SecretStore` sidecar（`<DataDir>/plugin-secrets/<id>.json`，dir 0o700 / file 0o600，原子 rename）+ `Split(schema, raw)` / `Merge(base, secrets)`；CLI/Desktop 写回时把 secret 分流到 sidecar，`config.json` 保持无明文；`pluginInitRequest`（CLI）与 `buildInitRequest`（Desktop）在 Init 前合并 sidecar，插件端拿到完整 `req.Settings`；`uninstall --purge` 同步删除 sidecar。
- **CLI `mow plugin config`**：新增 `list / get / set / unset / schema [--json]` 子命令族，全部按 schema 校验、脱敏后展示；[apps/cli/plugin_config.go](./apps/cli/plugin_config.go) + 单测覆盖 secret 隔离 & list 不泄漏。
- **Desktop 配置 UI**：`PluginsPage` Installed tab 每行新增 Settings 按钮 → `SettingsDrawer` 抽屉；根据 schema 渲染 enum 下拉 / boolean 复选框 / secret password 输入 / integer number；新增 `GetPluginSchema / GetPluginSettings / SetPluginSettings` 三个 Wails 方法；[bindings.ts](./apps/desktop/frontend/src/bindings.ts) 类型齐全。
- **AI 插件 schema 加固**：[plugins/ai/plugin.json](./plugins/ai/plugin.json) 补齐 `providers[].options.*` 的完整 schema，包括 `api_key { secret: true }`、`timeout_seconds` / `retry_*` 默认值与范围。
- **PVE 只读参考插件**：新增 [plugins/pve](./plugins/pve/) 独立 module（`replace ../../sdk`），涵盖 `cluster.status / node.list / vm.list / vm.status / lxc.list / vm.{start,stop,reboot} / lxc.{start,stop,reboot}` 共 11 条命令；HTTP client 采用 PVE API Token（`PVEAPIToken=<id>=<secret>`），支持 `insecure_tls` + 自定义 timeout；`settingsSchema.endpoints[].token_secret` 打上 `secret: true`，可选 `token_secret_env` 走环境变量；错误码 `PVE_UNAUTHORIZED / PVE_NOT_FOUND / PVE_UPSTREAM / PVE_UNREACHABLE / PVE_ENDPOINT_MISSING / PARAM_INVALID / ENCODE_FAILED / PVE_DECODE_FAILED / PVE_REQUEST_INVALID / PVE_BAD_REQUEST`；`commands_test.go` 起 `httptest.NewTLSServer` 覆盖成功路径 / 401 / 404 / 缺 token / 未知 endpoint / stop force=true 走 `/status/stop`、force=false 走 `/status/shutdown`；`manifest_test.go` 保证 Manifest 与运行时 Metadata / Commands 完全对齐。
- **CI / Release 兼容矩阵接入**：[ci.yml](./.github/workflows/ci.yml) 的 build / vet / unit-test / gosec 循环全部扩展到 `plugins/pve`；[release.yml](./.github/workflows/release.yml) 新增 `Build PVE Plugin` step；[scripts/build-catalog.go](./scripts/build-catalog.go) 默认 `-plugins ssh,docker,ai,pve`；`release.yml` 传参同步；[scripts/release-smoke.{sh,ps1}](./scripts/release-smoke.sh) Phase 1 加入 `pve` 校验；[scripts/lint.sh](./scripts/lint.sh) / [lint.ps1](./scripts/lint.ps1) 加入 `plugins/pve`；`go.work` 加入 `./plugins/pve`。

### v0.5.1 P0–P3 — 生命周期完整链路 + 本地 Catalog + Desktop Marketplace + Release Catalog

- **P0 · Catalog → Install/Update 打通**：新增 `core/plugin.Download`（支持 http/https/file，sha256 强校验，tar.gz/zip/裸 json，路径穿越 + symlink 拒绝，`MaxBytes` 默认 256 MiB）与 `Installer`（把 `catalog.Client + Download + Lifecycle` 串成 `Install/Update`）；CLI `mow plugin install|update` 现在同时接受本地包路径与 `id[@version]` catalog 引用（`--path` / `--catalog` 可强制），E2E `plugin_install_e2e_test.go` 覆盖静态 httptest catalog + tar.gz artifacts 走 `install → list → update → uninstall --purge` 全链路，checksum 篡改场景不留半成品。
- **P1 · Desktop 接入 Catalog**：`apps/desktop/plugin_catalog.go` 新增 Wails 方法 `ListCatalogSources / RefreshCatalog / SearchCatalog / InstallPluginFromCatalog / UpdatePluginFromCatalog`；`PluginsPage` 拆成 Installed / Marketplace 双 tab，Marketplace 提供搜索输入 + 多源刷新 + 表格（Latest / Installed / Version 下拉 / Install 或 Update 按钮）+ ReleaseDetails 面板；`plugin_catalog_test.go` 端到端覆盖 list/refresh/search/install + 无源报错。
- **P2 · 官方 Catalog 与 Release**：新增 `scripts/build-catalog.go`（读 `plugins/<id>/plugin.json` + release artifacts 生成 `catalog.json`，缺任何平台产物立即失败）；`.github/workflows/release.yml` 新增 `catalog` job（`needs: build` → 生成 → 上传独立 artifact），`smoke` 现在 `needs: [build, catalog]` 下载 catalog；Release Smoke Phase 2 新增 `plugin catalog refresh → search → install ssh → uninstall --purge`（bash + pwsh 双端）；`config.Default()` 预置 `official` catalog 源指向 `releases/latest/download/catalog.json`。
- **P3 · 文档收尾**：新增 [v0.5.1 验收清单](./docs/v0.5.1-acceptance-checklist.md)；[docs/plugin-system.md §8 Catalog & Distribution](./docs/plugin-system.md#8-catalog--distributionv051) 覆盖数据流 / Schema / Client 语义 / Installer / CLI / Desktop / Release 工作流 / 稳定错误码扩展；[roadmap.md](./docs/roadmap.md) 与 [development-plan-v0.5-v1.0.md §4.3.1](./docs/development-plan-v0.5-v1.0.md#431-v051-发布门槛) 门槛勾选。

### v0.5.1 P1 — 本地插件生命周期（早期批次）

- 新增 `core/plugin.Lifecycle`，提供本地包安装、枚举、启用、禁用和健康诊断。
- 安装在 `PluginsDir` 同盘临时目录复制并重新执行完整 Manifest/checksum 校验，成功后通过目录 rename 原子激活；拒绝包内符号链接和覆盖既有安装。
- 插件启停状态持久化到 `PluginsDir/.state`；显式禁用的插件不会再被 CLI 命令隐式加载。
- CLI 新增 `mow plugin list|install|enable|disable|doctor`，其中 list/doctor 支持 JSON 输出。
- 新增生命周期单元测试和 CLI 本地安装闭环测试，覆盖重复安装、checksum 篡改诊断及启停状态。

## [v0.5.0] - 2026-07-12

### 插件平台化 · 地基

v0.5 拆分为三个独立可 tag 的子版本；本轮完成 v0.5.0 的全部实现。

- **Plugin Manifest 规范**（[sdk/manifest/plugin.schema.json](./sdk/manifest/plugin.schema.json)）
  - JSON Schema draft 2020-12，覆盖 `manifestVersion` / `id` / `name` / `version` / `compatibility` (`core` + 可选 `sdk` / `protocol`) / `platforms[]` / `connectionTypes[]` / `permissions[]` / `commands[]` / `settingsSchema` / `recipes[]` / `workflows[]` / `dataVersion` / `migrations[]` / `source` / `signature`
  - `platforms[].checksum` 强制 `sha256:<hex64>`；所有相对路径禁止 `..` / `/` / `\` 穿越
- **`sdk/manifest` Go 包**（[sdk/manifest/manifest.go](./sdk/manifest/manifest.go) + [compatibility.go](./sdk/manifest/compatibility.go) + [package.go](./sdk/manifest/package.go)）
  - `Load(dirOrFile)` / `Parse(bytes)`：严格 `DisallowUnknownFields`，兼容 UTF-8 BOM，拒绝末尾多余 JSON
  - 逐字段业务校验：所有失败返回 `*sdk.Error{Code: "PLUGIN_MANIFEST_INVALID", Details.field}`
  - `MatchMetadata(sdk.Metadata)` 与运行时元信息比对
  - `CheckCompatibility(core, sdk, protocol)` 三层 semver 约束求解；零依赖手写 SemVer 2.0.0 求解器（`>=/<=/>/</=/!=` + `,` AND + `*`），pre-release 顺序遵循规范
  - `ValidatePackage(dir)`：entrypoint 存在性 + 真实 SHA-256 vs 声明 + recipes/workflows 路径存在性；返回逐项 `PackageReport`
  - 覆盖率 **81.4%**
- **`mow plugin validate` CLI**（[apps/cli/plugin.go](./apps/cli/plugin.go)）
  - `<path>` 支持文件或目录；`--json` 稳定机器可读 schema；`-v/--verbose` 打印每条通过
  - 稳定错误码：`PLUGIN_MANIFEST_INVALID` / `PLUGIN_CHECKSUM_MISMATCH` / `PLUGIN_ENTRYPOINT_MISSING`
  - 7 个 CLI 用例覆盖 happy text / happy JSON / verbose / invalid manifest / checksum mismatch / missing entrypoint / path not found
- **官方插件补齐 plugin.json**（[plugins/ssh](./plugins/ssh/plugin.json) / [plugins/docker](./plugins/docker/plugin.json) / [plugins/ai](./plugins/ai/plugin.json)）
  - SSH：6 commands，5 平台矩阵
  - Docker：13 commands（含 dangerous + 4 streaming），5 平台矩阵
  - AI：3 commands + `settingsSchema.providers[].kind` 枚举
  - 三份 `manifest_test.go` 保证 Manifest ⇄ runtime `Metadata()` 一致 + commands 集合完全对齐
  - SSH/Docker 的运行时 `Metadata.CoreVersion` 升级为 `>=0.4.0,<0.6.0` 与 Manifest 保持一致
- **运行时两道 Manifest 关卡**（[core/plugin/manifest_gate.go](./core/plugin/manifest_gate.go)）
  - 新 API：`ManifestGate{CoreVersion, SDKVersion, ProtocolVersion, Logger}`，Zero value 从 `sdk/version` + `sdk.Handshake` 取默认
  - `LoadFromPackage(dir, gate)`：静态校验 → `CheckCompatibility`（关卡 1，不启动子进程）→ 选平台 entrypoint → `loadBinary` → `MatchMetadata`（关卡 2，失败即刻 `Close`）
  - `Manager.RegisterFromPackage`：一步完成 + 注册失败自动 `Close` 子进程
  - `sdk/pluginclient.NewLoadedPlugin` 新增公共构造器，供测试与未来 in-process 官方插件复用
  - 8 个新增用例，覆盖 happy / 关卡 1 拦截 / 关卡 2 关闭子进程 / entrypoint 缺失 / 无平台条目 / Register 冲突回滚 / 默认值；`core/plugin` 覆盖率 **80% → 81.8%**
- **Manifest 包端到端闭环**
  - 正式布局：`<PluginsDir>/<id>/plugin.json + bin/<entrypoint>`；CLI/Desktop 统一包优先加载
  - 运行时启动前复用 `ValidatePackage` 校验真实 SHA-256；篡改包不会启动子进程
  - v0.5.x 兼容旧式平铺插件并输出弃用警告，计划 v1.0 移除
  - Release 按目标平台裁剪 Manifest、注入 tag 版本和真实 checksum，再生成 archive checksum
  - 三平台 Smoke 对 SSH/Docker/AI 全部执行 `mow plugin validate`，并通过 Manifest Gate 动态启动 AI
- **文档**
  - [docs/plugin-system.md](./docs/plugin-system.md) 状态升级为 **Living**，新增 §6 Plugin Manifest（含字段表 + 加载 + validate CLI 用法）与 §7 兼容性协商（约束语法 + 两道运行时关卡 + 稳定错误码 + 官方 Manifest 索引）
  - [docs/v0.5.0-acceptance-checklist.md](./docs/v0.5.0-acceptance-checklist.md) 状态由 Planned 更新为 Implemented
  - roadmap / development-plan 保持三子版本拆分
- **无破坏性 SDK / Plugin Protocol 变更**：Manifest 仅补充，运行时 gRPC 协议、`sdk.Metadata` / `CommandSpec` 均保持向后兼容

## [v0.4.1] - 2026-07-11

### GA 工程化收尾

- 新增根 `VERSION` 与 `sdk/version` 统一版本源；CLI、Desktop、SSH/Docker/AI 官方插件和前端统一为 `0.4.1`
- CLI 新增 `mow version`；Desktop 标题和 Wails binding 暴露相同版本
- SDK 新增公共契约与 gRPC 边界测试，覆盖插件校验、错误、AI JSON、枚举、握手、连接信封和转换往返
- 配置文件新增 schema version；无版本的 v0.3/v0.4 配置安全迁移，未来版本明确拒绝
- Release Workflow 校验 tag 与 `VERSION`，通过 ldflags 注入版本
- 新增 Linux amd64、Windows amd64、macOS arm64 Release archive 安装 Smoke；真实解压 CLI/AI 插件并执行 `mow ai providers`
- 完整清单见 [docs/v0.4.1-acceptance-checklist.md](./docs/v0.4.1-acceptance-checklist.md)

## [v0.4.0] - 2026-07-11

### v0.4.0 AI 可用闭环

- **OpenAI-compatible Provider**（[plugins/ai/openai.go](./plugins/ai/openai.go)）
  - `base_url` / `api_key_env` / 默认模型 / 自定义请求头；一次性 + 流式 Chat；tool_calls 支持
  - 尊重 `context.Cancel` / `context.Timeout`；401/403/400 直接失败，429/5xx 映射为稳定错误码
  - **有上限退避重试**（`retryPolicy`）：默认 3 次，Base=500ms → 1s → Cap=5s，仅对 `sdk.Error.Retryable=true` 生效；sleep 期间尊重 ctx 取消
  - 配置项：`retry_max_attempts` / `retry_base_backoff_ms` / `retry_max_backoff_ms`
  - 假 HTTP Server 契约测试，不依赖公网密钥
- **宿主 AI Orchestrator**（[core/ai/](./core/ai/)）—— host-side tool-use 闭环
  - `Orchestrator`：多轮 chat + tool_call 编排；并发安全；无状态
  - **Tool 目录自动派生**（P0-1）：`buildToolSpec` 从 `CommandSpec` 派生，非 allowlist / 非 Read / Streaming / `ai.*` 递归 / 缺 InputSchema 均在构造期拒收；模型无法声明宿主未认可的工具
  - **五道护栏**（P0-2）：`MaxRounds`（默 8）/ `MaxCallsPerRound`（默 4）/ `MaxTotalCalls`（默 16）/ `MaxResultBytes`（默 64 KiB）/ `Timeout`（默 120s），触发即返回 `*ai.Error` 稳定错误码
  - **结果截断标记**：超限时改为 `...[truncated]`，模型可据此识别
  - **role=tool 消息**携带 `sdk.Error.Code`，让模型据错重规划
- **参数递归脱敏**（P0-3，[core/command/redact.go](./core/command/redact.go)）
  - 从「顶层字段」升级为递归实现：嵌套 object / array items / 整颗 object 标 sensitive 全支持
  - Orchestrator 在追加到对话历史前对 `tool_call.Args` 做副本脱敏，**原始 Args 仍传给 Command Engine**（真实调用不受影响）
- **完整决策链路审计**（[core/ai/audit.go](./core/ai/audit.go)）
  - `Auditor` 接口 + 5 类事件：`ai.loop.start` / `ai.round.start` / `ai.round.end` / `ai.tool.call` / `ai.loop.end`
  - 保证「无论正常结束 / 拒收 / 超限 / provider 报错，`LoopEnd` 恰好发一次」，`FinishReason` 携带稳定错误码
  - 事件字段：`session_id` / `round` / `parent_audit_id` / `audit_id` / `tool_name` / `duration_ms` / `result_bytes` / `truncated` / `error_code` / `args_digest`（经 Redactor 脱敏）
  - 内置 `SlogAuditor`：Rejected/AI_* → WARN；其他 → INFO；产出可被 OTLP/SQLite handler 复用
  - `emit` 用 `recover` 兜住 auditor panic，绝不影响主流程
- **AI 配置双端共用**（[core/config/config.go](./core/config/config.go)）
  - 新增 `Config.AI` = `AIConfig`：`allowed_tools[]` + `max_rounds` / `max_calls_per_round` / `max_total_calls` / `max_result_bytes` / `timeout_seconds`
  - 默认 `allowed_tools` 为空 → 纯对话模式（v0.4 最保守初值）
- **CLI 接入**（[apps/cli/app.go](./apps/cli/app.go) + [apps/cli/ai.go](./apps/cli/ai.go)）
  - `App.Orchestrator()` 懒加载工厂，`sync.Once` 记忆化
  - **`mow ai ask` 改走 orchestrator**：自动携带 `SlogAuditor` + `command.RedactParams` + 上限护栏
  - `--json` 输出改为完整 `ChatResponse`（含 usage / finish_reason）
- **Desktop 接入**（[apps/desktop/ai.go](./apps/desktop/ai.go) + [apps/desktop/frontend/src/pages/AIPage.tsx](./apps/desktop/frontend/src/pages/AIPage.tsx)）
  - 新增 `App.AIAsk(AIAskInput) → AIAskResult`：非流式一次性对话，返回 `Response + Rounds + ToolCalls`
  - 新增 `App.AIStatus() → AIStatus`：暴露宿主 orchestrator 构造结果，UI 加载时预热
  - 前端 Ask 按钮走 `AIAsk`；`UsageBadges` 显示 `rounds:` / `tools:` / `tokens:` / `finish_reason` 四个 pill 徽章
  - **配置错误红色横幅**（`.ai-config-alert`）：orchestrator 构造失败（Dangerous / Streaming / Recursive / Unknown Tool）时在 AI 页面顶部立即展示错误码 + 修复建议；纯对话模式显示黄色 `.ai-config-note`
  - **Retry 按钮**（`.ai-retry`）：错误红条内嵌，一键重放最后一条 user message，Retry 也走 orchestrator（审计一致）
  - Send（流式）与 Ask（orchestrator）共用 usage state，Send 路径由 `ai:<sid>:finish` 事件回填 tokens + finish
- **E2E 端到端测试**（[tests/e2e/ai_e2e_test.go](./tests/e2e/ai_e2e_test.go)）
  - Mock provider 走完整 orchestrator 循环，断言 4 类事件序列
  - OpenAI-compatible provider + httptest fake server：happy path / usage 抵达
  - 429 → 200 有上限退避重试（1ms base，3 次上游请求恰好触达 200）
  - tool_call 白名单命令：宿主真实执行 in-process `probePlugin.info` 并把结果作为 `role=tool` 回填模型
- **人工验收剧本**（[docs/v0.4-manual-acceptance.md](./docs/v0.4-manual-acceptance.md)）：4 场景（对话主路径 / 取消 / 限流 / 工具调用）+ 配置模板 + 打 tag 命令
- **测试与覆盖率**
  - `core/ai`：**31 个测试，85.0% 覆盖率**（含审计事件序列、拒收分支、Slog 级别、panic 安全）
  - `core/command`：71.3% 覆盖率（递归脱敏 + 4 个新用例）
  - `plugins/ai`：72.3% 覆盖率（含 5 个 retry 用例）
  - `apps/cli`：7 个 orchestrator 用例覆盖 Dangerous / Streaming / Recursive AI / Unknown Tool / ctx cancel / provider 错误码传播
  - Desktop 前端：`AIPage.test.tsx` 5 个测试，覆盖 Send/Ask/error/config-alert/Retry 路径
  - `tests/e2e/ai_e2e_test.go`：4 个用例覆盖 mock / OpenAI happy / 429 retry / tool_call 白名单

### v0.4.0 AI Plugin 骨架

- **`sdk/ai.go`**（新增，[sdk/ai.go](./sdk/ai.go)）：定义 AI Provider 抽象
  - `Provider` 接口：`Name()` / `Capabilities()` / `Chat()` / `ChatStream()`
  - `ProviderCapabilities`：Chat / ChatStream / ToolCalls / Models
  - `ChatMessage` / `ChatRequest` / `ChatResponse` / `ChatUsage`：字段命名对齐 OpenAI 兼容 API 惯例
  - `ToolCall` / `ToolSpec`：tool-use / function calling 契约（v0.4.1 补齐闭环）
  - `ChatStreamSink` 回调三元组：`OnDelta` / `OnToolCall` / `OnFinish`
  - `RoleXxx` / `FinishXxx` 字符串常量，避免拼写错误
- **`plugins/ai`**（新 module，[plugins/ai/](./plugins/ai/)）：
  - [main.go](./plugins/ai/main.go)：`AIPlugin` + Settings `providers[]` 装配；未配置时默认挂 `mock`，保证 `ai.list_providers` 至少返回一项
  - [providers.go](./plugins/ai/providers.go)：`mockProvider` 实现 Chat / ChatStream，输出前缀 `[mock] `，逐 4 字符切片模拟流式；无网络依赖
  - [commands.go](./plugins/ai/commands.go)：三条 Command
    - `ai.list_providers`（Read）：按 name 升序列出 provider + capabilities
    - `ai.chat`（Read）：一次性对话；缺 messages / 未知 provider / capability 缺失均返稳定错误码
    - `ai.chat_stream`（Execute + Streaming）：`streamSink` 把 `OnDelta` → `Stdout`、`OnToolCall` → `Event`、`OnFinish` → `Finish`
  - [commands_test.go](./plugins/ai/commands_test.go)：覆盖率 **76.6%**，10+ 测试覆盖 Init 装配 / 未知 kind / 重复 name / name 默认 / 稳定顺序 / echo 主路径 / 参数校验 / 未知 provider / ExecuteStream 边界 / stream delta+finish
  - [go.mod](./plugins/ai/go.mod)：仅依赖 `mow/sdk`，与其它 plugin 一致
- **配套**：
  - [go.work](./go.work) 追加 `./plugins/ai`
  - [.github/workflows/ci.yml](./.github/workflows/ci.yml) 三个循环（build / vet / test）纳入 `plugins/ai`
  - [docs/ai-plugin.md](./docs/ai-plugin.md)：v0.4 设计文档（分层 / 命令 / 权限 / v0.4.0/0.4.1/0.5 交付范围）

## [v0.3.1] - 2026-07-10

v0.3.1 稳定性补丁：Docker Plugin 能力对齐（Windows npipe / TLS exec）+ Workflow 历史稳健性（轮转 + 跨进程锁）+ 覆盖率与 CI 收尾。完整明细见 [docs/roadmap.md#v03-Docker-Plugin--Docker-Dashboard--Workflow-增强-🎯-RC-就绪待发布](./docs/roadmap.md)。

### 新增

- **JSONL 跨进程文件锁**（[core/workflow/history/flock_unix.go](./core/workflow/history/flock_unix.go) + [flock_windows.go](./core/workflow/history/flock_windows.go)）
  - Unix：`golang.org/x/sys/unix.Flock(fd, LOCK_EX)` 阻塞式独占；进程崩溃时内核自动释放
  - Windows：`golang.org/x/sys/windows.LockFileEx(handle, LOCKFILE_EXCLUSIVE_LOCK, 0, 0xFFFFFFFF, 0xFFFFFFFF)` 锁整个文件；handle 关闭自动释放
  - `JSONLStore.Save` 打开 fd 后立即尝试独占锁，写完 defer unlock；文件锁失败静默降级到单进程 mutex 保护（不影响功能）
  - `golang.org/x/sys` 从 indirect 升为 direct dependency（core module）
  - 新增 [flock_test.go](./core/workflow/history/flock_test.go)：`TestMain` 检测 `MOW_HISTORY_FLOCK_WORKER=1` 走子进程分支；`TestJSONLStore_CrossProcessSaveNoInterleave` 通过 `os.Executable` 自我 exec 出 4 个 worker 各写 50 条 400+ 字节记录，主进程验证共 200 行 + 每行独立 JSON 可解析
- **Docker E2E 接入常规 pipeline**（[.github/workflows/ci.yml](./.github/workflows/ci.yml)）
  - `docker-e2e` job 触发条件从"仅 `workflow_dispatch`"扩展为三源：
    - `push:main` → 一律运行，作为主干健康的一级信号
    - `workflow_dispatch` → 保留 `only=all/docker-e2e` 手动开关
    - `pull_request` → 通过 [dorny/paths-filter@v3](https://github.com/dorny/paths-filter) 只在触及 `plugins/docker/**` / `tests/e2e/docker*_test.go` / `apps/desktop/docker_*.go` / `.github/workflows/ci.yml` 时才跑
  - 独立 `concurrency: docker-e2e-${github.ref}` group + `cancel-in-progress: true`，防止同一 ref 内两个触发源同时抢 daemon；顶层 `ci-${ref}` 继续管 test/race
  - `Decide run` step 把 filter 输出翻译为 `gate.outputs.run`；后续所有真实步骤（setup-go / docker pull / build plugin / run e2e）都用 `if: steps.gate.outputs.run == 'true'` 门控；`Post-cleanup` 仍走 `if: always()` 保证兜底清理
- **TLS `docker.exec` raw-hijack**（[plugins/docker/client.go](./plugins/docker/client.go) `dialHijack` + [exec.go](./plugins/docker/exec.go)）
  - `engineClient` 新增 `tlsCfg *tls.Config` 字段；`newEngineClient` 在 tcp+TLS 分支同时挂给 `http.Transport.TLSClientConfig` 与 `c.tlsCfg`
  - `dialHijack` 拨号后若 `c.tlsCfg != nil` 就在 raw conn 上做 `tls.Client(conn, cfg).HandshakeContext(ctx)`；握手失败返回稳定错误码 `DOCKER_TLS_HANDSHAKE_FAILED`（retryable）
  - SNI 与证书校验用 `buildTLSConfig` 里预设的 `ServerName`；HTTP 请求行的 Host 头保持 "docker" 占位
  - `plugins/docker/exec.go` 移除 TLS pre-guard；仅保留非 Windows 平台上的 npipe pre-guard
  - 桌面 [`App.DescribeDockerTarget`](./apps/desktop/docker_stage3.go)：`tcp+TLS` 分支从 "exec_supported=false" 改为 "true"（合并到 unix/tcp）
  - 桌面 [`App.DockerExecOpen`](./apps/desktop/docker_stage3.go)：移除 TLS 硬拒分支
  - 新增 [hijack_tls_test.go](./plugins/docker/hijack_tls_test.go)：`httptest.NewTLSServer` + 手工 hijack 响应，验证 TLS raw-hijack 主路径（成功读到 payload）与握手失败错误码
- **Windows `npipe://` 真实实现**（[plugins/docker/npipe_windows.go](./plugins/docker/npipe_windows.go) + [npipe_other.go](./plugins/docker/npipe_other.go)）
  - 引入 `github.com/Microsoft/go-winio v0.6.2`，通过 `winio.DialPipeContext` 拨号；`\\.\pipe\xxx` 与 `//./pipe/xxx` 两种形式均可
  - 平台文件 build tag 隔离：非 Windows 构建不会引入 winio 依赖，`CGO_ENABLED=0` 跨平台交叉编译保持零 CGO
  - `plugins/docker/client.go` 的 `newEngineClient` npipe 分支：Windows 装配 DialContext；其它平台返回 `DOCKER_NPIPE_UNSUPPORTED`
  - `plugins/docker/exec.go` 的 pre-guard：npipe 仅在 `!npipeSupported` 时拒绝（改自 v0.3 的无条件拒绝）
  - 桌面 [`App.DescribeDockerTarget`](./apps/desktop/docker_stage3.go)：新增 `runtime.GOOS == "windows"` 判定
  - 桌面 [`App.DockerExecOpen`](./apps/desktop/docker_stage3.go)：`npipe && GOOS!=windows` 时拒绝调用（双重防御）
  - 前端 [`TargetsPage`](./apps/desktop/frontend/src/pages/TargetsPage.tsx)：不再阻止保存 `npipe://` target；输入框下方黄字改为 "npipe:// 仅在 Windows 桌面上可用（v0.3.1）；非 Windows 客户端保存后 exec 会被禁用"
  - 新增 [`npipe_test.go`](./plugins/docker/npipe_test.go)：跨平台 dial helper 行为断言
- **`plugins/docker` 覆盖率**：59.6% → **76.0%**（新增 [plugins/docker/coverage_test.go](./plugins/docker/coverage_test.go) + hijack_tls_test.go + npipe_test.go）
  - Metadata / Commands / HealthCheck / Init / Shutdown 元信息断言
  - 每个 CommandHandler 的 `Spec()` + `Execute` vs `ExecuteStream` 不支持分支
  - `statusCodeToErrorCode` 全码表 / `mapTransportError` cancel / timeout / retryable 三分支 / `buildTLSConfig` bad CA / bad key / 成功
  - `newEngineClient` npipe（平台分叉） / unknown scheme / unix 构造成功三分支
  - `docker.exec` npipe pre-guard / TLS 放行、参数校验（缺 id / 缺 cmd / 反序列化失败）
  - `classifyRegistryError` unauthorized / denied / not found / unknown 全分支
  - `mapReadErr` nil / EOF / canceled / timeout / generic 五分支
  - `postJSON` bad body 与 dst=nil 成功路径
  - `dialHijack` TLS 成功 + 握手失败两条路径
- **Workflow JSONL 历史**（[core/workflow/history/jsonl.go](./core/workflow/history/jsonl.go)）
  - 新增 `RotateOptions{MaxBytes, MaxKeep}` + `NewJSONLStoreWithRotate`；零值保持旧行为向后兼容
  - `readAllWithRotated` 跨主文件 + `.1..N` 轮转文件读取；`Get` / `List` 都会看到历史
  - `doRotate` 倒序 rename + `highestRotated` 只扫真实存在的历史；`MaxKeep>0` 时超限文件被 prune
  - 抗回归测试新增 7 个：轮转生效 / MaxKeep prune / 100 并发无交错 / 脏行跨轮转跳过 / 空行容忍 / 关闭态不轮转 / 负值 clamp

### 计划中

- v0.4.1：真实 AI Provider（OpenAI / Anthropic）+ tool-use 闭环 + Desktop AI Chat 面板
- v0.5：PVE / Kubernetes / 数据库 Plugin、MCP 双向对接、Marketplace 雏形

## [v0.3.0] - 2026-07-10

v0.3 主线：**Docker Plugin + Docker Dashboard + Workflow 引擎增强**。完整验收见 [docs/v0.3-acceptance-checklist.md](./docs/v0.3-acceptance-checklist.md)。

### 新增 — Docker Plugin（`plugins/docker`）

- 独立 gRPC 子进程插件，复用 `sdk/pluginserve`，进程隔离、独立崩溃
- 传输协议一次覆盖三种：`unix://` / `tcp://` / `tcp:// + TLS`（`ca` + `cert` + `key` 三件套）
- **v0.3 第一阶段**（lifecycle + 只读）：`docker.list` / `docker.inspect` / `docker.start` / `docker.stop` / `docker.restart` / `docker.logs`（流式；8 字节 mux 头解码，TTY 原始透传）
- **v0.3 第三阶段**（完整生命周期 + 镜像分发 + 交互式 exec）：
  - `docker.rm`（**Dangerous** 权限 · Command Engine 二次确认 + 应用层 `Confirmed=true` 双重护栏）
  - `docker.pull` / `docker.push`（流式 progress · chunked JSON lines · `X-Registry-Auth` 头；`errorDetail` 触发 `sdk.Error` 不调 Finish）
  - `docker.exec`（双向流：`create` → `start` hijack → mux/raw → exit code；TTY 场景原始透传；`SignalWinch` 走 `POST /exec/{id}/resize`）
  - 只读补齐：`docker.images` / `docker.volumes` / `docker.networks`
- 错误码体系：`DOCKER_DIAL_FAILED` / `DOCKER_NOT_FOUND` / `DOCKER_CONFLICT` / `DOCKER_UNAUTHORIZED` / `DOCKER_ENGINE_ERROR` / `DOCKER_REGISTRY_ERROR` / `CONFIRMATION_REQUIRED` 等
- `core/connection.DockerCredentials`（`connection.Type = "docker"`）：host / api_version / TLS 三件套；`Sealer` (AES-256-GCM) 加密到 `Target.EncryptedCredentials`

### 新增 — Docker Dashboard（`apps/desktop`）

- **v0.3 第二阶段**：容器列表（state 徽标 · `all=true` 默认可切换）→ Inspect 抽屉（只读 JSON）→ 流式 logs 面板（`docker:logs:<sid>:{stdout,stderr,exit}` 事件契约）→ 生命周期弹窗（`Confirmed=true` 才下发）
- **v0.3 第三阶段**：Remove 弹窗（force / volumes 可选，`running/restarting` 自动预勾 force；Dangerous 双重护栏）+ Exec 交互式终端（xterm.js + 首帧 winch 同步尺寸）+ Images / Volumes / Networks 只读 Tab
- Wails 后端 API：`DockerList` / `DockerInspect` / `DockerLifecycle` / `DockerLogsOpen`/`Close` / `DockerRm` / `DockerImages` / `DockerVolumes` / `DockerNetworks` / `DockerExecOpen`/`Write`/`Resize`/`Close`
- 前端 Tab 路由：仅在 active target 类型为 `docker` 时可点

### 新增 — Workflow 引擎增强（分五批合入）

1. **`when: <expr>` 条件分支**（[workflow.md §7.4.1](./docs/workflow.md#741-when-条件分支v03-第一批)）：表达式复用 expr-lang，无需 `${}` 包裹；`false` → `Skipped`（OK=true，不写 out）；求值失败 → `WHEN_EVAL` 中断
2. **`retry: { max, backoff, max_backoff, exponential }`**（[§7.4.2](./docs/workflow.md#742-retry-单-step-重试v03-第二批)）：`max ∈ [0, 20]`；`WHEN_EVAL` / `INTERPOLATE` 不参与重试；`StepResult.Attempts` + `PhaseRetry` 事件观测
3. **执行历史持久化 · JSONL**（[§7.4.3](./docs/workflow.md#743-执行历史持久化v03-第三批)）：`<data_dir>/workflow-runs.jsonl` append-only；`RunID = run-<16 hex>`；`Store` 抽象保留 SQLite 切换空间；写盘错误不冒泡到 `Runner.Run`；CLI `mow workflow history list|show` + Desktop History 面板 & 详情抽屉
4. **`on_failure` / `rollback` 声明式回滚**（[§7.4.4](./docs/workflow.md#744-on_failure--rollback-声明式回滚v03-第四批)）：逆序遍历，只对**成功过**的 step 调用 `compensate`；补偿失败不嵌套回滚 / 不 retry；`Result.Rollback` 全量快照 + `PhaseRollback` 事件
5. **`parallel: true` 组内并行**（[§7.4.5](./docs/workflow.md#745-parallel-true-组内并行v03-第五批)）：连续 `parallel: true` 归为同一组；fail-fast 取消同组兄弟；组内**禁止**引用兄弟 `steps.<sibling>.out.*`（`LoadBytes` / `Validate` 静态强制）；`OnStep` 回调 mutex 序列化；`Result.Steps` 按声明顺序追加

### 新增 — CLI / Desktop

- `apps/cli`：`mow workflow history list [--limit N] [--workflow ID] [--json]` / `mow workflow history show <run-id> [--json]`
- `apps/cli`：Workflow 进度打印新增 `⤼ skipped` / `↻ retry 1/3 after 500ms` / `↩ rollback deploy 100ms` 三类图标
- `apps/desktop`：`WorkflowPage` 底部新增可折叠的 History 面板，Run 结束自动刷新；点击历史行 → 抽屉展示 inputs / steps / audit id / error code / rollback 记录

### 变更

- `core/command`：根据 `CommandSpec.InputSchema` 执行 JSON Schema 输入校验，为无效 Schema 与不匹配参数提供稳定错误码
- CI（`.github/workflows/ci.yml`）：三个 module（core / apps / plugins）逐个 build / vet / test；追加 `plugins/docker` build & test；Desktop 前端强制 `npm ci && npm run build`；Linux + Windows 双矩阵；新增 **`docker-e2e` job**（`workflow_dispatch → only=docker-e2e/all` 手动触发；Linux 真实 daemon；预编译 plugin 传给测试；`label=mow-e2e=1` 兜底清理）
- Release（`.github/workflows/release.yml`）：新增 **Docker Plugin 跨 5 平台构建**；每个 `.tar.gz` 生成 `.sha256`，release job 汇总生成 `SHA256SUMS`；`body_path` 按 tag 动态解析（`v0.3.x → docs/v0.3-acceptance-checklist.md`），不再固定指向 v0.1
- E2E（`tests/e2e`）：新增 [docker_helpers_test.go](./tests/e2e/docker_helpers_test.go) 与 [docker_e2e_test.go](./tests/e2e/docker_e2e_test.go)，覆盖 `docker.list` / lifecycle / `docker.logs` / `docker.pull` / `docker.exec` / `docker.rm` 六个真实 daemon 场景；默认 `t.Skip`，需 `MOW_DOCKER_E2E=1` + 可达 daemon 才实跑
- 统一所有 Go module、`go.work`、CI 与文档最低 Go 版本至 **1.25**
- README / Roadmap / docs 状态：v0.3 从"下一版"调整为"RC（发布前修正中）"；`docs/workflow.md` 状态升级为 **Implemented (v0.3)**；`docs/docker-plugin.md` 状态升级为 **Implemented**；新增 [`docs/v0.3-acceptance-checklist.md`](./docs/v0.3-acceptance-checklist.md)

### 测试

- `core/workflow` 覆盖率 **92.5%**（v0.2：91.2%）
- `core/workflow/history` 覆盖率 **81.2%**
- `core/command` 覆盖率 **71.0%**（新增 InputSchema 校验路径）
- `plugins/docker` 覆盖率 **59.6%**（临时门槛；v0.3.1 目标 ≥ 70%）
- `plugins/ssh` 单测 PASS
- `tests/e2e` 全量 PASS（含 `TestWorkflow_DeployStaticSite_EndToEnd`）
- `go vet ./...` 全部 module 无告警
- 前端 `tsc --noEmit` 通过
- `go test -race`：本机跳过（无 gcc），CI 双平台矩阵兜底

### 已知边界（发布前保留 · v0.3.1 补齐）

- **Windows `npipe://`**：三重护栏
  - `TargetsPage` 保存前拦截 `npipe://` scheme + 输入框实时提示
  - 桌面后端 `DockerExecOpen` 二次校验
  - 插件层 `newEngineClient` / `docker.exec` 返回稳定错误码 `DOCKER_NPIPE_UNSUPPORTED` / `DOCKER_EXEC_NPIPE_UNSUPPORTED`
- **TLS + `docker.exec` 的 raw-hijack**：双重护栏
  - 桌面新增 `App.DescribeDockerTarget` 返回 `exec_supported` + `exec_unsupported_reason`；`DockerExecDrawer` 挂载即调用，`exec_supported=false` 时禁用 Start
  - 插件层 `docker.exec` 检测 `Scheme=tcp && (TLSVerify||TLSCA!="")` 立即返回 `DOCKER_EXEC_TLS_UNSUPPORTED`
- **Release CI 产物缺口**：已修复 —— [release.yml](./.github/workflows/release.yml) 追加 `plugins/docker` 全 5 平台构建、`.sha256` + `SHA256SUMS` 校验文件、`body_path` 按 tag 动态解析（`v0.3.x → docs/v0.3-acceptance-checklist.md`）

### 依赖

- `plugins/docker`：仅标准库 `net/http`（不引入官方 `github.com/docker/docker` SDK，避免上百 MiB 依赖）
- `core`：延续 v0.2 的 `gopkg.in/yaml.v3` + `github.com/expr-lang/expr`

## [v0.2.0] - 2026-07-10

### 新增

- **Workflow Engine（MVP）**：`core/workflow` 全新模块
  - 声明式数据模型：`Workflow` / `Step` / `Input` / `InputType` + `Validate()`
  - YAML DSL Loader：`LoadFile` / `LoadBytes` / `LoadReader`，严格模式（`yaml.Decoder.KnownFields(true)`）——未知字段一律报错
  - 变量插值：`${inputs.<name>}` / `${steps.<id>.out.<field>}`，基于 [`github.com/expr-lang/expr`](https://github.com/expr-lang/expr)，支持算术 / 三元 / 混合字符串；未定义变量报 `InterpolationError` 携带偏移量
  - Runner 主循环：顺序执行 + 任一失败中止 + Data 反序列化挂到 `scope.Steps[id].Out` 供后续步骤消费
  - OnStep 三阶段回调（`start` / `finish` / `error`）
  - 依赖倒置的 `CommandExecutor` / `RecipeExecutor` 接口，方便 CLI / Desktop 各自注入 Adapter
- **CLI**：新增 `mow workflow validate|run`
  - 彩色进度打印：`▶ upload (cmd:ssh.upload) ... ✓ 42ms`
  - `--input k=v` / `--inputs-json` / `--target` / `--json` / `--no-color`
  - Workflow 依赖插件自动 Enable（含 Recipe 内 step 的 Plugin）
- **Desktop**：新增 Workflow 页面
  - 拖拽或选择 `.yaml` → 后端 `WorkflowValidate` → 依据 `inputs` 生成表单
  - Run 后通过 wails 事件 `workflow:<sess>:step` / `:done` 实时推送日志
  - `int` / `bool` 输入前端强转
- **示例与文档**：
  - [`examples/workflows/deploy-static-site.yaml`](./examples/workflows/deploy-static-site.yaml) + README
  - [`docs/workflow.md`](./docs/workflow.md) 更新为 **Implemented (MVP)** 状态，新增 §7 MVP 已实现字段表 + §8 最小示例
  - [`docs/roadmap.md`](./docs/roadmap.md)：v0.2 打勾 ✅，v0.3 目标明确为 **Docker Plugin + Docker Dashboard + Workflow 增强**
  - [`docs/v0.2-acceptance-checklist.md`](./docs/v0.2-acceptance-checklist.md)：完整验收清单
- **E2E**：`tests/e2e/workflow_e2e_test.go` 复用 fake SSH server 跑通 `deploy-static-site.yaml` 的最小子集，断言插值命中 fake server 收到的命令行

### 测试

- `core/workflow` 单测：**48 项 PASS**，覆盖率 **91.2%**
- `core` 全量：7/7 包 PASS
- `tests/e2e` 全量：PASS（含 `TestWorkflow_DeployStaticSite_EndToEnd`）
- `plugins/ssh` 单测：PASS
- `go build ./...`：`apps/cli` / `apps/desktop` 均通过
- `go vet ./...`：三个 module 无告警
- 前端 `tsc --noEmit`：通过

### 依赖

- `core` 新增：`gopkg.in/yaml.v3 v3.0.1`、`github.com/expr-lang/expr v1.17.8`

### 边界（v0.2 未实现，见 [docs/workflow.md §7.5](./docs/workflow.md#75-尚未实现-v03)）

- `parallel: true` / `when: <expr>` / `on_failure` / `retry` / `notify` / `rollback`
- 单 step 级 `target` 覆盖
- 执行历史 SQLite 持久化
- 上述全部规划在 **v0.3+**

## [v0.1.0] - 2026-07-10

### 新增

- **Command Engine**：统一执行链路核心，支持参数校验、权限控制、审计日志、危险操作二次确认
- **Plugin Framework**：基于 hashicorp/go-plugin 的 gRPC 子进程插件系统，SDK 提供完整 Go 抽象与 protobuf 定义
- **SSH Plugin**：官方 SSH 插件，支持 exec / shell / SFTP / ping / 连接池，Ed25519 私钥鉴权与 passphrase 解密
- **CLI (Cobra)**：`mow target`（目标管理）、`mow ssh`（交互式 Shell + SIGWINCH）、`mow run`（单次执行）、`mow recipe`（Recipe 运行）
- **Desktop (Wails v2)**：React + TypeScript + xterm.js，Terminal / SFTP / Targets 三个页面
- **Recipe System**：内置 Recipe 注册与 Runner，支持步骤顺序执行与失败中止
- **Connection Manager**：支持 Upsert / Get / Open / Delete / Persist，Keystore 加密存储凭证
- **结构化日志**：基于 `log/slog`，支持多级别与 JSON 输出
- **跨平台支持**：Windows / Linux，CI 双平台通过（golangci-lint / go test / race / gosec）
- **E2E 测试体系**：Fake SSH Server + Test Rig，覆盖 exec / recipe / SFTP / Shell / 连接池等 23 个用例
- **16 篇设计文档**：架构总纲、各模块 RFC、验收清单、Roadmap 等

[Unreleased]: https://github.com/mow/mow/compare/v0.5.4...HEAD
[v0.5.4]: https://github.com/mow/mow/compare/v0.5.3...v0.5.4
[v0.4.1]: https://github.com/mow/mow/compare/v0.4.0...v0.4.1
[v0.4.0]: https://github.com/mow/mow/releases/tag/v0.4.0
[v0.3.1]: https://github.com/mow/mow/releases/tag/v0.3.1
[v0.3.0]: https://github.com/mow/mow/releases/tag/v0.3.0
[v0.2.0]: https://github.com/mow/mow/releases/tag/v0.2.0
[v0.1.0]: https://github.com/mow/mow/releases/tag/v0.1.0
