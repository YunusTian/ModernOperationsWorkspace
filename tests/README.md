# 测试

- 单元测试就近放在各 module 的 `*_test.go` 中
- 本目录用于**跨 module 的集成测试与端到端测试**

## 目录

```
tests/
├── integration/     # Core + Plugin 集成测试
└── e2e/             # 完整链路（CLI / Desktop → Core → Plugin → Target）
```

## 常用命令

```powershell
# 只跑 e2e（约 6s）
cd tests/e2e ; go test -count=1 ./...

# 加 -race（需要 CGO_ENABLED=1 + gcc/clang）
../../scripts/race.ps1
```

## Race 检测

Go 的 `-race` 依赖 CGO 与 C 编译器。开发机若无 gcc / TDM-GCC / mingw-w64，
`scripts/race.ps1` 会自动 **skip** 并返回 exit 0，便于 CI 矩阵跨平台。

启用步骤：

1. 安装 TDM-GCC 或 mingw-w64
2. 确保 `gcc` 在 PATH 中
3. `.\scripts\race.ps1`

脚本会依次对 `core`、`plugins/ssh`、`tests/e2e` 三个 module 跑 `go test -race -count=1 ./...`。
