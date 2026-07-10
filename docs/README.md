# MOW 文档索引

> 顶层架构总纲请见根目录 [Architecture.md](../Architecture.md)。
> 本目录下为按模块拆分的 RFC 骨架，每份 RFC 独立演进。

## RFC 列表

| RFC | 主题 | 对应 Architecture.md 章节 |
| --- | --- | --- |
| [vision.md](./vision.md) | 项目愿景与平台愿景 | 一、十三 |
| [design-principles.md](./design-principles.md) | 设计哲学与原则 | 二、十二 |
| [architecture.md](./architecture.md) | 总体架构总览 | 三、七 |
| [connection-manager.md](./connection-manager.md) | 连接管理 | 4.1 |
| [plugin-system.md](./plugin-system.md) | Plugin Manager + SDK + 开发规范 | 4.2、五、八 |
| [command-engine.md](./command-engine.md) | 命令引擎 | 4.3 |
| [recipe.md](./recipe.md) | Recipe 引擎 | 4.4 |
| [workflow.md](./workflow.md) | Workflow 引擎 | 4.5 |
| [ssh-plugin.md](./ssh-plugin.md) | SSH 插件（v0.1） | 4.2 / 五 |
| [docker-plugin.md](./docker-plugin.md) | Docker 插件（v0.3 全阶段：lifecycle / logs / rm / pull / push / exec / 只读列表） | 4.2 / 五 |
| [ai.md](./ai.md) | AI 架构 | 六 |
| [ui.md](./ui.md) | UI 层设计 | UI Layer |
| [permission.md](./permission.md) | 权限模型 | 九 |
| [observability.md](./observability.md) | 日志与可观测 | 十 |
| [roadmap.md](./roadmap.md) | 路线图 | 十一 |

## RFC 撰写约定

- 每份 RFC 使用统一头部：状态 / 版本 / 更新日期 / 作者
- 状态取值：`Draft` → `Review` → `Accepted` → `Implemented` → `Superseded`
- 大改动通过新增 RFC 覆盖旧 RFC，而不是原地重写历史
- 先写 RFC 再写代码；实现完成后回填"实现说明"章节
