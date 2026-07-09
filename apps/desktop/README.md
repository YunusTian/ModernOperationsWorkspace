# Wails 桌面客户端

MOW 桌面客户端骨架（Wails v2 + React + TypeScript + xterm.js）。

## 目录结构

```
apps/desktop/
├── main.go              # Wails 入口，embed frontend/dist
├── app.go               # 前端可调用的 backend API
├── shell_stream.go      # sdk.Stream 桌面端适配，桥接 xterm ↔ ssh.shell
├── wails.json           # Wails 项目配置
├── go.mod / go.sum
├── frontend/
│   ├── package.json     # React + xterm + vite
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   ├── src/
│   │   ├── main.tsx
│   │   ├── App.tsx           # 侧边栏 + 三页导航
│   │   ├── bindings.ts       # 后端方法/事件 TypeScript 包装
│   │   ├── styles.css
│   │   └── pages/
│   │       ├── TargetsPage.tsx
│   │       ├── TerminalPage.tsx
│   │       └── SftpPage.tsx
│   └── dist/                 # vite build 产物（占位）
└── build/                    # wails 打包输出（gitignore）
```

## 前置

- Go ≥ 1.22（当前实测 1.26 也可）
- Node ≥ 18
- Wails CLI：`go install github.com/wailsapp/wails/v2/cmd/wails@latest`（首次使用）
- 已经把 SSH 插件构建到 `${DataDir}/plugins/ssh.exe`（`DataDir` 默认为 `%USERPROFILE%\.mow` 或 `~/.mow`）
  - 编译命令：`cd plugins/ssh && go build -o "$env:USERPROFILE\.mow\plugins\ssh.exe" .`

## 开发

```powershell
cd apps/desktop
# 首次
cd frontend ; npm install ; cd ..

# 启动
wails dev
```

`wails dev` 会：

- 编译 Go 后端并注入 `window.go.main.App`
- 拉起 vite（前端热更新）
- 打开桌面窗口

## 三页最小可用

1. **Targets**：`connMgr.List` / `Upsert` / `Delete` / `PingTarget`（走 `ssh.exec "true"`）
2. **Terminal**：xterm.js ↔ `ssh.shell` 双向流，事件桥：
   - 前端 → 后端：`ShellWrite(sessionID, base64)` / `ShellResize` / `ShellClose`
   - 后端 → 前端：`shell:<sid>:stdout|stderr|exit`
3. **SFTP**：`ssh.sftp.list` / `sftp.upload` / `sftp.download`
   - 上传/下载用绝对路径；文件对话框留待后续接入 `runtime.OpenFileDialog`

## 打包

```powershell
wails build
```

产物位于 `build/bin/`。
