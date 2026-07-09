# RFC: Recipe Engine

- 状态：Draft
- 版本：v0.1
- 更新日期：2026-07-09
- 相关章节：Architecture.md § 4.4

---

## 1. 定义

Recipe = **由若干 Command 组成的、预定义、已测试的操作**。

## 2. 特点

- 完全**不依赖 AI** 也可运行
- 参数经过验证，安全可控
- 是 AI 调用的**首选载体**

## 3. 示例

| Recipe ID | 组成 |
| --- | --- |
| `system.cpu` | `top -bn1 \| head` |
| `system.disk` | `df -h` |
| `docker.status` | `docker ps` + `docker stats` + `docker images` |

## 4. Recipe 声明草案（YAML）

```yaml
recipe:
  id: docker.status
  description: 汇总 Docker 运行状态
  permission: read
  steps:
    - command: docker.list
    - command: docker.stats
    - command: docker.images
  output:
    strategy: merge          # merge / last / custom
```

## 5. 待讨论

- [ ] Recipe 是否允许包含控制流（if / loop）——建议交给 Workflow
- [ ] Recipe 是否允许跨 Plugin 组合
- [ ] Recipe 参数与 Command 参数的映射约定
- [ ] Recipe 输出的标准化 Schema
