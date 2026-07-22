# RFC: Plugin Authoring Guide（第三方插件开发者旅程）

- 状态：Living
- 版本：v0.5.4-draft
- 更新日期：2026-07-17
- 相关文档：[plugin-system.md](./plugin-system.md) · [SDK README](../sdk/README.md) · [plugins/pve](../plugins/pve/)（参考实现）

---

本文档面向**从未接触 MOW 仓库的 Go 开发者**，目标是在 30 分钟内跑通「脚手架 → lint → package → 本地安装 → 命令调用」的完整闭环。

## 1. 前置条件

- Go 1.25.0 及以上
- 已安装 MOW CLI（`mow version` 可用）
- 熟悉基本的 Go module 用法

MOW 的插件是**独立进程 gRPC 子进程**（基于 hashicorp/go-plugin），本文档不涉及 in-process 插件。

## 2. 三分钟脚手架（`mow plugin init`）

```powershell
mow plugin init acme --dir ./mow-acme-plugin
cd ./mow-acme-plugin
```

生成物：

```text
mow-acme-plugin/
├── plugin.json     # Manifest（v0.5.0 schema）
├── main.go         # pluginserve.Serve 入口 + hello 命令
├── go.mod          # module example.com/mow-acme-plugin
└── README.md       # 下一步指引
```

要点：

- 插件 ID 必须匹配 `^[a-z][a-z0-9_-]{1,63}$`（[plugin_dev.go isValidPluginID](../apps/cli/plugin_dev.go#L133)）
- 默认 `compatibility.core = ">=0.5.0,<0.6.0"`；发布前请对齐目标 Core 版本
- `--force` 允许覆盖已有目录；否则第二次运行会直接报 "already exists"
- entrypoint 的 checksum 是占位符 `sha256:00...`，`mow plugin package` 会替换为真实哈希

## 3. Manifest 静态检查（`mow plugin lint`）

`lint` 只解析 `plugin.json` 并跑 schema + 语义校验，**不触磁盘**，适合放进 pre-commit：

```powershell
mow plugin lint --dir .
# OK  acme@0.1.0 (Acme)
```

若 Manifest 违规，会以稳定错误码退出：

```powershell
mow plugin lint --dir . --json
# {
#   "ok": false,
#   "error": { "code": "PLUGIN_MANIFEST_INVALID", "details": { "field": "id", ... } }
# }
```

`lint` 与 `mow plugin validate`（v0.5.0 就绪）的区别：

| 命令 | 静态 Manifest | entrypoint 存在性 | checksum 匹配 | 场景 |
|---|---|---|---|---|
| `plugin lint` | ✅ | ❌ | ❌ | 编辑期反馈（无二进制）|
| `plugin validate` | ✅ | ✅ | ✅ | 打包后 / CI 门禁 |

## 4. 实现命令

在 [main.go](../apps/cli/plugin_dev.go#L184) 生成的骨架里已经带了一个 `hello` 命令。要添加新命令：

1. 在 `main.go` 里新增 `type fooCmd struct{}` 并实现 `Spec / Execute / ExecuteStream`
2. 把 `&fooCmd{}` 加进 `Commands()` 返回值
3. 在 `plugin.json` 的 `commands[]` 里同步声明（`sdk.Metadata` 与 Manifest 不一致会被两道 Manifest Gate 拦截）

参考真实实现：

- [plugins/pve/commands.go](../plugins/pve/commands.go)：11 条只读 / lifecycle 命令
- [plugins/ssh/commands.go](../plugins/ssh/commands.go)：exec / upload / download / ping / shell（含 Streaming）
- [plugins/docker/exec.go](../plugins/docker/exec.go)：Dangerous + Streaming + hijack 复合场景

### 4.1 权限语义

- `read` —— 只读，可被 AI Orchestrator 白名单纳入
- `write` —— 有副作用但可逆
- `execute` —— 长时命令 / shell / 交互
- `dangerous` —— 破坏性操作，Command Engine 强制要求 `Confirmed=true`

### 4.2 稳定错误码

命令抛错请返回 `*sdk.Error{Code, Message, Details, Retryable}`；AI Orchestrator 会根据 `Code` 决定是否让模型重规划。参考 [plugins/pve/commands.go](../plugins/pve/commands.go) 的 `PVE_UNAUTHORIZED / PVE_NOT_FOUND / PARAM_INVALID` 等命名。

## 5. SDK Conformance 测试（`sdk/conformance`）

在插件仓库里放一个 `conformance_test.go`：

```go
package main

import (
    "testing"

    "github.com/mow/mow/sdk/conformance"
)

func TestConformance(t *testing.T) {
    conformance.Run(t, conformance.Suite{
        Plugin: &plugin{},
        Cases: []conformance.Case{
            {CommandID: "hello"},
        },
    })
}
```

`conformance.Run` 会自动：

1. 跑 `sdk.Validate`（Metadata / CommandSpec 结构校验）
2. 驱动完整生命周期：`Init → HealthCheck → 你的 cases → Shutdown`
3. 对 Dangerous 命令自动断言：未确认时必须返回 `sdk.ErrConfirmationRequired`
4. 为 Streaming 命令挂载 [FakeStream](../sdk/conformance/fake_stream.go)，用 `Push / StdoutChunks / Events / FinalData` 断言输出

不需要真实 gRPC、不需要外部依赖；进程内驱动。

## 6. 打包（`mow plugin package`）

```powershell
mow plugin package --os linux --arch amd64
# building linux/amd64 → bin/mow-acme-plugin
# packaged D:\...\dist\mow-acme-plugin-linux-amd64.tar.gz
#          D:\...\dist\mow-acme-plugin-linux-amd64.tar.gz.sha256
# entrypoint checksum: sha256:abc123...
```

`plugin package` 做了三件事：

1. 用 `go build -trimpath -ldflags="-s -w"` 交叉编译入口二进制（`CGO_ENABLED=0`）
2. 重写 `plugin.json`：只保留当前目标的 `platforms[]` 条目，并注入真实 checksum
3. 打成 `mow-<id>-plugin-<os>-<arch>.tar.gz` + 同名 `.sha256`（layout 与 release artifact 完全一致）

flag 速览：

| flag | 默认 | 说明 |
|---|---|---|
| `--os / --arch` | 宿主 GOOS/GOARCH | 支持 linux/darwin/windows × amd64/arm64 |
| `--out` | `dist` | 输出目录 |
| `--version` | 保留 Manifest 版本 | CI 里可注入 tag 版本 |
| `--ldflags` | `-s -w` | 追加 ldflags |
| `--trimpath` | true | 关闭需显式 `--trimpath=false` |
| `--keep-staging` | false | 保留中间 staging 目录以便调试 |

## 7. 本地安装 → 调用闭环

```powershell
# 1. 从 tar.gz 或包目录本地安装
mow plugin install ./dist/mow-acme-plugin-linux-amd64.tar.gz
# Installed acme@0.1.0 (disabled).

# 2. 启用
mow plugin enable acme
# Enabled acme@0.1.0.

# 3. 调用
mow run acme.hello
# {"message":"hello from acme"}

# 4. 卸载（保留 state；--purge 才删）
mow plugin uninstall acme
```

Desktop 用户走 **Plugins → Marketplace → Install from file** 也能完成同一链路。

## 7.1 热重载（`mow plugin dev --watch`）

如果你正在密集迭代命令实现，`mow plugin dev` 把 §6 的 build + §7 的 install/update 合并成一步，还能自动监听源码变化：

```powershell
# 一次性：build → 安装到 PluginsDir → 自动 enable
mow plugin dev --dir .

# 或者开启 watch，改完保存自动重装
mow plugin dev --dir . --watch --interval 300ms
```

要点：

- 首次运行走 `Lifecycle.Install`；后续走 `Lifecycle.Update`（原子替换 + 失败回退）。装完自动 `Enable`，无需再敲 `mow plugin enable`
- 只支持 host GOOS/GOARCH —— Manifest 的 `platforms[]` 会被裁剪成当前平台条目，跨平台 dev 没有意义（会在参数校验阶段直接拒绝）
- 监听白名单：`*.go` / `*.yaml` / `*.yml` / `*.json` / `go.mod` / `go.sum`；自动跳过 `.git` / `vendor/` / `dist/` / `node_modules/`。若命中变更就重跑 build + Update
- 轮询实现（默认 500ms 间隔），不引入 fsnotify 依赖。Ctrl-C 可随时退出
- build 失败不会退出 watch —— 允许你改回来继续；`stderr` 会打印错误但循环继续

典型 dev 循环：

```text
[shell1] mow plugin dev --watch
         dev: building linux/amd64 → bin/mow-acme-plugin
         installed acme@0.1.0 (enabled).
         watching /home/you/acme (interval=500ms); press Ctrl-C to stop.

[edit main.go]

         dev: change detected at 2026-07-17T16:04:12Z
         dev: building linux/amd64 → bin/mow-acme-plugin
         updated acme@0.1.0 (enabled).

[shell2] mow run acme.hello
         {"message":"hello from acme"}
```

## 8. 发布到 Catalog（可选）

MOW 的 Catalog 只是一份静态 JSON（[core/plugin/catalog/catalog.go](../core/plugin/catalog/catalog.go)），你可以：

1. 把 `mow-acme-plugin-<os>-<arch>.tar.gz` 上传到任何 HTTP(S) 服务器（GitHub Release、S3、Nexus…）
2. 生成一份形如 [catalog.json](../scripts/build-catalog.go) 的入口
3. 用户在 `~/.mow/config.json` 的 `app.catalog.sources[]` 里加一条 `{"name":"acme", "url":"https://..."}`
4. `mow plugin catalog refresh` → `mow plugin install acme` 走 SHA-256 强校验的官方链路

## 9. 迭代小抄

| 场景 | 命令 |
|---|---|
| 修完 Manifest 想立刻校验 | `mow plugin lint --dir .` |
| 改完代码想立刻测（一步到位） | `mow plugin dev --dir .`（含 watch：加 `--watch`） |
| 想在 CI 上门禁 | `go test ./...`（conformance）+ `mow plugin validate ./dist/staging`（entrypoint + checksum）|
| 排查 Manifest / Metadata 不一致 | 查看 [core/plugin/manifest_gate.go](../core/plugin/manifest_gate.go) 的两道关卡 |

## 10. 已知限制（v0.5.4）

- `plugin dev --watch` 采用 mtime 轮询（默认 500ms），改动大目录时 CPU/IO 敏感场景需要手动调大 `--interval`；未来若有需求会切换到 fsnotify
- 暂无 Manifest 迁移工具：`dataVersion` / `migrations[]` 由插件自行处理
- Windows arm64 未纳入官方 Release 矩阵

后续 DX 增强规划见 [开发计划 §4.5.1](./development-plan-v0.5-v1.0.md#451-v054插件开发者体验v053-后追加可延后到-v06-前)。
