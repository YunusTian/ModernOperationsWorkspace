# RFC: Plugin System（Manager + SDK + 开发规范）

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
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
