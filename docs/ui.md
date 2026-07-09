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

## 3. 桌面客户端候选技术

| 方案 | 优点 | 缺点 |
| --- | --- | --- |
| Avalonia | .NET 原生跨平台、性能好 | 生态较新 |
| Tauri | 体积小、Web 前端复用 | Rust 学习成本 |
| Electron | 生态成熟 | 内存占用高 |
| WPF | Windows 体验最佳 | 非跨平台 |

## 4. 待讨论

- [ ] 是否引入 Web 版（浏览器直接访问 Core）
- [ ] Terminal 组件选型（xterm.js / 自研）
- [ ] Dashboard 是否支持自定义仪表盘 DSL
- [ ] AI Chat 与 Terminal 的联动方式
