# RFC: UI 层设计

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 三（UI Layer）

---

## 1. 核心原则

- UI **不放业务逻辑**
- UI **只调用 Command Engine**
- UI 之间可共享 Session / Context，但不共享内部实现

## 2. 三种交互形态

| 形态 | 定位 | 主要场景 |
| --- | --- | --- |
| **Terminal** | 传统终端体验 | SSH 会话、REPL、日志跟随 |
| **Dashboard** | 可视化面板 | Docker / PVE / 监控 |
| **AI Chat** | 自然语言入口 | 意图 → Recipe / Workflow |

## 3. 桌面客户端技术栈（v0.1 已定）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 桌面框架 | **Wails v2** | Go 后端同进程 + WebView 前端 |
| 前端框架 | **React 18 + TypeScript** | 生态最完整 |
| 构建工具 | **Vite** | Wails 默认支持 |
| UI 组件库 | **shadcn/ui + Tailwind CSS** | 无运行时依赖、可定制 |
| 状态管理 | **Zustand**（轻） / TanStack Query（异步） | 按需引入 |
| Terminal | **xterm.js** + `xterm-addon-fit` / `xterm-addon-web-links` | 事实标准 |
| 路由 | **React Router** | |
| 图标 | **lucide-react** | |
| 与 Core 通信 | Wails Runtime（Go ↔ JS 绑定） | 桌面端；CLI 走 Go 直调 |

## 4. 三种交互形态实现草案

| 形态 | 前端实现 | 备注 |
| --- | --- | --- |
| Terminal | xterm.js + Wails 流式绑定 | 通过 Command Engine 的 gRPC server-stream 桥接 |
| Dashboard | React 组件 + TanStack Query | Recipe 结果驱动 |
| AI Chat | React + Markdown 渲染 | v0.4 才启用 |

## 5. 待讨论

- [ ] 是否同时提供 Web 版（Core 独立 HTTP Server + 同一前端）
- [ ] Dashboard 是否引入声明式 DSL（YAML → 组件）
- [ ] AI Chat 与 Terminal 的联动方式（引用某次执行结果继续对话）
- [ ] 深色 / 浅色主题切换与 xterm.js 主题联动
