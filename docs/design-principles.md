# RFC: 设计哲学与原则

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 二、§ 十二

---

## 1. 设计哲学

### 1.1 AI 是增强，不是核心

所有 AI 能完成的操作，都必须能够通过传统方式（GUI / CLI / Recipe / Workflow）完成。

### 1.2 执行链路唯一

```
User / AI / CLI / GUI / API
              │
              ▼
        Command Engine
              │
              ▼
       Workflow / Recipe
              │
              ▼
           Plugin
              │
              ▼
        Connection
              │
              ▼
           Target
```

### 1.3 AI 永远不直接生成 Shell 去操作服务器

AI 只能：
- 理解用户意图
- 选择或组合已有 Recipe / Workflow / Command
- 调用并解释结果

### 1.4 架构约束（最高优先级）

> **Core 永远不依赖 AI，AI 永远依赖 Core。**

## 2. 设计原则（写入 CONTRIBUTING.md）

| 原则 | 说明 |
| --- | --- |
| **Core First** | 核心能力先于 UI，所有界面都调用同一套 Core |
| **AI Optional** | AI 是可选能力，产品不能依赖 AI 才能使用 |
| **Plugin Everything** | 新能力优先做成插件，而不是直接修改 Core |
| **Workflow over Script** | 把经验沉淀为可复用 Workflow |
| **API First** | Core 对外提供统一 API，CLI / GUI / AI 都通过 API |
| **Safety First** | 危险操作必须权限检查与二次确认 |
| **Observable** | 每个动作都可追踪、可审计、可回放 |
| **Domain Driven** | 抽象领域模型，而不是协议 / Shell |

## 3. 强约束（Review 时必须核对）

- [ ] Core 模块 import 图中不允许出现 AI Plugin
- [ ] UI 层不允许直接调用 Plugin / Connection
- [ ] 任何新能力优先以 Plugin 形态提交
- [ ] 危险操作必须显式声明 `permission: dangerous`
