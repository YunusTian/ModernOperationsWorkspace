# 测试

- 单元测试就近放在各 module 的 `*_test.go` 中
- 本目录用于**跨 module 的集成测试与端到端测试**

规划：

```
tests/
├── integration/     # Core + Plugin 集成测试
└── e2e/             # 完整链路（CLI / Desktop → Core → Plugin → Target）
```
