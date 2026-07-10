# Workflow 示例

本目录存放示例的 Workflow YAML 声明，供 CLI / Desktop / Marketplace 参考。

## deploy-static-site.yaml

最小可运行的静态站点发布 Workflow，只依赖 `ssh.exec`，无需额外插件：

1. **backup** — 若远端目录存在，打成 tar 备份到 `/tmp/backup-<site>.tgz`；否则 mkdir。
2. **upload** — 演示远端目录准备（占位；生产可换成 `ssh.sftp.upload`）。
3. **health** — 通过 `ss -ltn` 探测目标端口是否在监听。

### CLI 用法

```bash
# 只解析 + 校验
mow workflow validate examples/workflows/deploy-static-site.yaml

# 实际执行（需要已注册的 SSH Target）
mow workflow run examples/workflows/deploy-static-site.yaml \
  --target=srv1 \
  --input site=hello \
  --input local_dir=/home/me/dist \
  --input remote_dir=/var/www/hello \
  --input health_port=8080
```

### Desktop 用法

打开 **Workflow** 标签页 → 拖拽 / 选择 `deploy-static-site.yaml` → 依据 `inputs` 声明填写表单 → **Run**，实时查看每一步 `▶/✓/✗` 的日志。

### 变量与插值

- `${inputs.site}` / `${inputs.remote_dir}` — 引用 Workflow inputs。
- `${steps.<id>.out.<field>}` — 引用上一步的结构化输出（例如 `${steps.upload.out.bytes_sent}`）。

详见 [docs/workflow.md](../../docs/workflow.md) 与 [PR3 vars.go](../../core/workflow/vars.go)。

### 生产化建议（v0.2+ 补齐）

- 备份/上传/回滚彻底解耦，用 `onFailure` 声明式回滚（Roadmap）。
- 上传改走 `ssh.sftp.upload`，避免对 `ssh.exec` 传 stdin 的限制。
- Health 步骤使用 `retry: { max, backoff }`（Roadmap）。
