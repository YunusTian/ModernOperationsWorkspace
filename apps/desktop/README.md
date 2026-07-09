# Wails 桌面客户端

MOW 桌面客户端骨架（Wails v2 + React + TypeScript）。

## 目录规划

```
apps/desktop/
├── main.go              # Wails 入口
├── app.go               # App 结构体（后续新增）
├── wails.json           # Wails 项目配置（wails init 后生成）
├── frontend/            # React + TS 前端（wails init 后生成）
│   ├── src/
│   ├── index.html
│   ├── package.json
│   └── vite.config.ts
└── build/               # 构建产物（已加入 .gitignore）
```

## 初始化

首次开发时执行（**当前尚未执行**）：

```powershell
cd apps/desktop
wails init -n mow-desktop -t react-ts
```

初始化后请：
- 手动移除 Wails 生成的示例代码
- 保留本 README、`go.mod`、`main.go` 骨架
- 将 `wails.json` 中的 `name` 设为 `mow-desktop`
