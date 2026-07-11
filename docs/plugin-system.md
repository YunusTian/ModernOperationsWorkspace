# RFC: Plugin System（Manager + SDK + 开发规范）

- 状态：Living
- 版本：v0.2（v0.5.0 补充 Manifest 与兼容性协商）
- 更新日期：2026-07-11
- 相关章节：Architecture.md § 4.2、§ 五、§ 八

---

## 1. Plugin Manager

### 1.1 生命周期

```
Load → Enable → Disable → Unload
```

### 1.2 注册项

每个插件必须注册：

- Metadata（元信息）
- Commands（能力）
- Recipes（预定义操作）
- Workflows（编排）
- Permission（权限声明）
- Settings（配置项）

## 2. Plugin SDK

统一接口：

```
Plugin
├── Metadata      # id / name / version / author / description
├── Commands      # 最小执行单元
├── Recipes       # 预定义组合操作
├── Workflows     # 编排流程
├── Permission    # Read / Write / Execute / Dangerous
└── Settings      # 用户配置项（连接、路径、开关）
```

### 2.1 Metadata

- `id`（唯一标识，例：`ssh`、`docker`）
- `name` / `version` / `author` / `description`
- 依赖声明（依赖的 Core 版本、其他 Plugin）

### 2.2 Command

最小执行单元：

- `ssh.exec`
- `ssh.upload` / `ssh.download`
- `docker.listContainer`
- `docker.pullImage`
- `pve.startVM`

要求：
- 输入 / 输出 / 错误全部**可序列化**
- 单一职责，不承担编排逻辑

### 2.3 Recipe

由多个 Command 组成，**无 AI 参与也能直接运行**。

示例：`server.status` → `cpu` + `memory` + `disk` + `network`

### 2.4 Workflow

多 Recipe 编排 + 分支 + 回滚 + 通知（详见 [workflow.md](./workflow.md)）。

### 2.5 Plugin 声明示例（YAML）

```yaml
plugin:
  id: ssh
  name: SSH
  version: 0.1.0
  author: mow

commands:
  - id: exec
    permission: execute
  - id: upload
    permission: write
  - id: download
    permission: read

recipes:
  - id: system.cpu
  - id: system.memory
  - id: system.disk

workflows:
  - id: deploy.dotnet
  - id: deploy.node
```

## 3. 插件开发规范

### 3.1 必须遵守

- 插件**不得**直接操作 UI
- 插件**不得**依赖 AI
- 插件**只能**注册 Commands / Recipes / Workflows

### 3.2 Docker Plugin 示例

```
Docker Plugin
├── Commands
│   ├── docker.list
│   ├── docker.pull
│   ├── docker.stop
│   ├── docker.logs
│   └── docker.rm            # permission: dangerous
├── Recipes
│   ├── docker.health
│   ├── docker.cleanup
│   └── docker.status
└── Workflows
    └── docker.deploy
```

## 4. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 加载机制 | **hashicorp/go-plugin（gRPC 子进程）** | Terraform 同款，进程隔离 + 独立崩溃 |
| 接口定义 | **Protocol Buffers**（`sdk/proto/plugin.proto`） | 语言无关，未来可支持 Rust/Python 插件 |
| 分发格式 | v0.1 单二进制文件（本地路径） | Marketplace 之后再定 |
| SDK 语言 | Go（首版） | 通过 gRPC 天然支持多语言 |

## 5. 待讨论

- [ ] 插件签名与来源校验（Sigstore / cosign？）
- [ ] 插件热更新与版本兼容策略（gRPC 协议版本号约定）
- [ ] Marketplace 分发格式（zip / oci artifact / git ref）
- [ ] 是否为官方 Plugin 提供进程内快速通道（当前默认统一 gRPC）
- [ ] WASM 沙箱作为不可信来源方案的引入时机

## 6. Plugin Manifest（v0.5.0）

v0.5.0 引入 `plugin.json` 作为插件包（plugin package）的静态元信息载体，覆盖插件从**打包 → 校验 → 加载 → 兼容性协商**的整段生命周期。

Manifest 的权威结构定义在 [sdk/manifest/plugin.schema.json](../sdk/manifest/plugin.schema.json)（JSON Schema draft 2020-12），运行时反序列化由 [sdk/manifest](../sdk/manifest) 提供。

### 6.1 推荐包结构

```text
plugin-package/
├── plugin.json          # Manifest，本节权威定义
├── bin/                 # 二进制入口（每平台一份）
├── schemas/             # 可选：InputSchema / OutputSchema 外置文件
├── recipes/             # 可选：内置 Recipe YAML
├── workflows/           # 可选：内置 Workflow YAML
└── docs/                # 可选：面向用户的文档
```

正式安装布局为：

```text
<PluginsDir>/
└── <plugin-id>/
    ├── plugin.json
    └── bin/
        └── <entrypoint>[.exe]
```

Release archive 解压后必须直接得到 `plugin.json + bin/`，不得要求用户改名或移动二进制。发布构建会把 Manifest 裁剪为当前目标平台，并注入该二进制的真实 SHA-256。

### 6.2 Manifest 字段

| 字段 | 是否必需 | 说明 |
| --- | --- | --- |
| `manifestVersion` | ✅ | 目前恒为 `1`。破坏性变更时递增 |
| `id` | ✅ | 全局唯一标识，与 `sdk.Metadata.ID` 完全一致；匹配 `^[a-z][a-z0-9_-]{1,63}$` |
| `name` | ✅ | UI 展示名 |
| `version` | ✅ | 语义化版本；与 `sdk.Metadata.Version` 完全一致 |
| `author` / `license` / `homepage` / `description` | ⚪ | 元信息 |
| `compatibility.core` | ✅ | 与 MOW Core 版本的 semver 约束，见 §7 |
| `compatibility.sdk` / `compatibility.protocol` | ⚪ | SDK / Plugin Protocol 层约束（缺省跳过该层校验） |
| `platforms[]` | ✅ | 每条包含 `os` / `arch` / `entrypoint`（包内相对路径）/ `checksum`（`sha256:<hex64>`） |
| `connectionTypes[]` | ⚪ | 声明本插件消费的 Connection 类型 |
| `permissions[]` | ⚪ | Manifest 层的粗粒度声明；与 CommandSpec.Permission 无交集但用于快速展示 |
| `commands[]` | ⚪ | 每条含 `id` / `permission` / `streaming` / `description`；用于 Marketplace 展示与静态检查 |
| `settingsSchema` | ⚪ | 内嵌 JSON Schema。v0.5.0 只校验其合法性，v0.5.2 由此驱动 CLI/Desktop 配置 UI |
| `recipes[]` / `workflows[]` | ⚪ | 每条含 `id` + 包内相对路径 |
| `dataVersion` / `migrations[]` | ⚪ | 数据格式版本与迁移入口 |
| `source` / `signature` | ⚪ | 发布来源与签名（v0.5 保留字段，v0.5.1 起启用） |

**安全约束**：所有相对路径（`platforms[].entrypoint` / `recipes[].path` / `workflows[].path`）不得以 `/` 开头、不得包含 `..` 或反斜杠，防止穿越到包外。

### 6.3 加载与解析

Manifest 加载走 [sdk/manifest](../sdk/manifest) 的两个函数：

```go
m, err := manifest.Load(packageDir)  // 目录或直接文件路径
m, err := manifest.Parse(data)       // 已在内存中
```

- 严格模式：`DisallowUnknownFields`，未知字段一律拒绝
- 拒绝末尾多余 JSON 值
- 兼容 UTF-8 BOM
- 逐字段业务校验：所有失败返回 `*sdk.Error{Code: "PLUGIN_MANIFEST_INVALID"}`，`Details.field` 精确到 `platforms[2].checksum` 这种粒度

### 6.4 `mow plugin validate`

在 Manifest 静态校验之外做磁盘级校验：

1. `platforms[].entrypoint` 在包内实际存在
2. entrypoint 的实际 SHA-256 与 Manifest 声明的 `checksum` 匹配
3. `recipes[].path` / `workflows[].path` 在包内实际存在

```powershell
mow plugin validate ./my-plugin-pkg           # 人类可读输出
mow plugin validate ./my-plugin-pkg --json    # 稳定机器可读 schema
mow plugin validate ./my-plugin-pkg -v        # 打印每一条通过的检查
```

失败时按稳定错误码退出：`PLUGIN_MANIFEST_INVALID` / `PLUGIN_CHECKSUM_MISMATCH` / `PLUGIN_ENTRYPOINT_MISSING`。

## 7. 兼容性协商（v0.5.0）

MOW 通过 Manifest 的三层 semver 约束实现兼容性协商：`core` / `sdk` / `protocol`。

### 7.1 约束语法

由 [sdk/manifest.ParseConstraint](../sdk/manifest/compatibility.go) 提供，语法故意收敛：

| 语法 | 含义 |
| --- | --- |
| `1.2.3` | 等价于 `=1.2.3` |
| `>=0.5.0` / `<=0.5.9` / `>0.4.0` / `<1.0.0` | 明确算子 |
| `=1.0.0` / `!=1.2.3` | 精确匹配 / 反匹配 |
| `>=0.5.0,<0.6.0` | 逗号 AND 组合 |
| `*` 或空串 | 通配符，任意版本 |

**不支持** caret (`^`) / tilde (`~`) / 前导 `v`。pre-release 按 SemVer 2.0.0 顺序（`1.0.0-rc.1 < 1.0.0`），build metadata (`+xxx`) 忽略。

### 7.2 两道运行时关卡

`core/plugin.LoadFromPackage(packageDir, gate)` 与 `Manager.RegisterFromPackage` 在启动子进程前后各设一道关卡：

```
manifest.ValidatePackage(dir)   静态结构 + entrypoint + checksum 校验
  ↓
CheckCompatibility(core, sdk, protocol)    ← 关卡 1：还没启动子进程
  ↓ 失败 → PLUGIN_INCOMPATIBLE（附 layer / actual / constraint）
resolveEntrypoint(GOOS, GOARCH)  选一条 platforms[]
  ↓ 失败 → PLUGIN_ENTRYPOINT_MISSING
loadBinary(entrypoint)           启动 gRPC 子进程
  ↓
MatchMetadata(runtime meta)              ← 关卡 2：拿到子进程 Metadata 立即比对
  ↓ 失败 → 关闭子进程 + PLUGIN_MANIFEST_MISMATCH
```

设计要点：

- **关卡 1** 让"已知不兼容的插件"完全不会付出进程 fork/gRPC 握手的代价
- **关卡 2** 一旦发现 Manifest 与运行时 `sdk.Metadata` 的 `id` 或 `version` 对不上，**必须**立即关闭子进程，避免泄漏
- 三层约束缺省时（如 Manifest 不声明 `compatibility.sdk`）跳过该层
- Runtime 版本从 [sdk/version.Version](../sdk/version/version.go) 与 [sdk.Handshake.ProtocolVersion](../sdk/handshake.go) 读取；`ManifestGate` 允许 apps 覆盖用于测试
- 实际应用入口统一调用 `plugin.LoadInstalled`：优先走包目录和 Manifest Gate。v0.5.x 为升级兼容保留旧式 `<PluginsDir>/<id>[.exe]` 平铺二进制加载，但会记录弃用警告；新安装和 Release 产物必须使用包目录。旧式入口计划在 v1.0 移除

### 7.3 稳定错误码

| Code | 触发点 | Details |
| --- | --- | --- |
| `PLUGIN_MANIFEST_INVALID` | Load / Parse / Validate | `field`, `reason` |
| `PLUGIN_MANIFEST_MISMATCH` | MatchMetadata | `field`(id/version), `manifest`, `runtime` |
| `PLUGIN_INCOMPATIBLE` | CheckCompatibility | `layer`(core/sdk/protocol), `actual`, `constraint` |
| `PLUGIN_CHECKSUM_MISMATCH` | ValidatePackage | `path`, `expected`, `actual` |
| `PLUGIN_ENTRYPOINT_MISSING` | resolveEntrypoint / ValidatePackage | `path` 或 `os`/`arch` |

这些 Code 是**契约的一部分**，CHANGELOG 会记录任何变更。CLI / Desktop / Workflow / AI 都应按 Code 做条件判断，不要 grep Message。

### 7.4 官方插件 Manifest 索引

- SSH：[plugins/ssh/plugin.json](../plugins/ssh/plugin.json)（6 commands）
- Docker：[plugins/docker/plugin.json](../plugins/docker/plugin.json)（13 commands，含 dangerous 与 streaming）
- AI：[plugins/ai/plugin.json](../plugins/ai/plugin.json)（3 commands + settingsSchema）

每个官方插件的 `manifest_test.go` 都会强校验 Manifest 与运行时 `Metadata()` 一致、Manifest.commands 与运行时 CommandHandler 集合互相对齐；避免运行时/Manifest 漂移。
