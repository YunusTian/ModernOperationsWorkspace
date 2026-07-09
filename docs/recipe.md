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

## 5. 技术选型（v0.1）

| 项 | 选型 | 说明 |
| --- | --- | --- |
| 声明格式 | **YAML** | 与 Workflow 保持一致 |
| 参数校验 | JSON Schema（转换自 YAML） | 与 Command 参数复用 |
| 输出合并 | 声明式 `strategy: merge / last / custom` | |

## 6. 待讨论

- [ ] Recipe 是否允许包含控制流（if / loop）——**v0.1 决定：交给 Workflow**
- [ ] Recipe 是否允许跨 Plugin 组合（v0.1 允许，但需权限并集校验）
- [ ] Recipe 参数与 Command 参数的映射约定
- [ ] Recipe 输出的标准化 Schema（考虑与 AI 消费对齐）
