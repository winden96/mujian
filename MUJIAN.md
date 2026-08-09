# 幕间 AI on NewAPI

本分支以 NewAPI `v0.12.14`（`6ff8c7a`）为固定上游基线，新增幕间 AI 的项目、剧本、分镜、画面、Skills、Agent 和积分钱包能力。NewAPI 的原版权、模块路径与 AGPL-3.0 声明保持不变。

当前部署版本源码：[winden96/mujian-newapi](https://github.com/winden96/mujian-newapi)。

## 本地启动

1. 复制 `.env.mujian.example` 为 `.env`，替换所有密钥与公共源码地址。
2. 执行 `docker compose -f docker-compose.mujian.yml up --build`。
3. 打开 `http://localhost:3000`，由 Root 在 NewAPI 渠道管理中配置上游渠道和模型价格。

普通用户不会在幕间导航中看到 Token、Playground 或渠道入口。Agent 和生图使用每个用户自动创建的隐藏 `mujian-internal` Token 回调本实例 relay，因此调用仍经过 NewAPI 的渠道选择、额度检查、结算、退款和用量日志。

## 生产约束

- `AGPL_SOURCE_URL` 必须指向包含当前部署版本完整源代码的公共仓库；`Dockerfile.mujian` 在该值缺失时构建失败。
- `MUJIAN_PRODUCTION=true` 时，服务也会在源码地址缺失时拒绝启动。
- 渠道密钥仅在 Root 渠道管理页录入。不得写入代码、Compose、浏览器存储或日志。
- 使用独立 PostgreSQL 与 Redis，并由 HTTPS 反向代理暴露单一应用实例。
- 升级只在独立 `upstream-sync/*` 分支合并并完成全链路测试后进入幕间分支。
