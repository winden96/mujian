# 管理员直接充值积分

## Planner

用户管理增加独立的“直接充值”操作，单次接受 1～1,000,000 整数积分，备注最多 200 个 Unicode 字符。复用 User 余额和 TopUp 订单，不调用支付渠道。

架构检查：你是一名高级架构开发工程师，能精确的知道代码Bug，禁止胡乱兜底糊弄，要切入到具体问题，以及从底层的角度思考问题。给出最优的解决方案。

## Generator

- 路径：用户管理 → 目标用户“直接充值” → 输入积分和备注 → 确认。弹窗显示实时读取的目标账号积分；长确认文案支持手机换行。
- `POST /api/user/:id/recharge` 使用 AdminAuth 和 CriticalRateLimit，接收 `{ "credits": 100, "remark": "客户补充积分", "request_id": "UUID" }`，返回 `{ "success": true, "data": { "order": WalletOrder, "quota": number, "credits": number } }`。
- 普通管理员可操作低等级账号；root 可操作其他账号。拒绝给自己、不存在、已删除的账号充值。禁用的目标账号可收款且仍保持禁用；操作管理员必须处于启用状态。
- 订单唯一标识由管理员 ID 和规范化 UUID 组成。订单与余额增量同事务提交；重复请求返回原订单，相同标识但目标账号、积分或规范化备注不同返回 409。普通非法输入返回 400，权限不足 403，目标不存在 404，数据库错误 500。
- SQLite 在读取前通过订单插入取得写锁；MySQL/PostgreSQL 使用数据库唯一约束和行锁处理并发。余额使用 SQL 原子增量，防止覆盖并发消费；提交后失效余额缓存。管理日志使用既有日志服务，事务内的订单保存完整操作人和备注，日志库故障不会丢失订单审计信息。
- TopUp 增加 `admin_id`、`admin_username`、`remark`。管理员订单使用 `source=mujian_wallet`、`payment_method=admin`、`status=success`，`money=0`，`amount` 为积分数，`credited_quota` 为实际内部额度。
- User 的 `quota`、`used_quota` 改为 BIGINT，支持超过 32 位整数范围的充值与后续消费累计。现有 AutoMigrate 在启动时迁移，旧订单新增字段使用零值/空字符串。
- 积分换算沿用钱包现有规则：73 积分对应 500,000 内部额度，入账按最小额度单位四舍五入。例如 100 万积分入账 6,849,315,068 内部额度，页面显示 1,000,000.00 积分。
- 浏览器以管理员和目标用户为键，将结果待确认的请求保存在 sessionStorage。关闭弹窗、刷新及网络失败重试保持请求 ID 和金额不变；确认成功后清除。后台记录显示管理员、目标用户 ID、备注，用户钱包显示“管理员充值”和到账状态。

## Evaluator

按 code-simplify → code-review → 验证顺序执行。保留独立 controller/service/model 功能文件，复用现有订单、缓存和日志机制；无新增支付兜底或第二套钱包。评审修正了 SQLite 锁升级、MySQL 重复请求快照读取、移动端确认按钮截断和管理账单列宽问题。

2026-09-13 本地验证：

- `go test ./model ./service/mujian ./router ./controller`：通过。覆盖积分边界、非法参数、权限、删除/禁用目标、订单审计、回滚、并发入账/消费、重复请求、真实会话登录和用户钱包 API。
- `go vet ./model ./service/mujian ./router ./controller`：通过。调整浏览器夹具初始化后，额外重跑 router 测试和 vet，通过。
- `go test -race ./service/mujian -run TestAdminRechargeIdempotencyAndConcurrentConsumption -count=1`：通过。
- 真 Redis 隔离实例：原充值和重复请求均使旧余额失效，后续读取返回新余额，通过。
- SQLite 旧数据迁移、重复迁移及大整数保存：通过。
- MySQL 9.6.0、PostgreSQL 17.10 临时数据库：从旧 INT/旧订单结构迁移、保留数据、100 万积分写入、12 个相同请求并发只到账一次、BIGINT 累计已用额度写入，均通过。
- 受影响前端文件 Prettier、ESLint 和 `bun run build`：通过。
- Playwright 真实浏览器连接生产路由/处理器及隔离 SQLite，不模拟充值 API 返回值：管理员登录、列表入口权限、1.5 积分禁用提交、100 万积分到账、管理账单操作人/备注、收款账号登录查看余额及“管理员充值 / 已到账”，通过。
- 响应丢失实验：转发并完成真实充值后中断浏览器响应，关闭重开弹窗重试；最终仍为一笔订单、6,849,315,068 内部额度，成功后 sessionStorage 请求已清除。
- 桌面及 390px 手机宽度检查：确认按钮完整显示账号/积分；账单使用固定列宽和横向滚动。

### 复现入口

在仓库根目录运行后端测试。SQL 可选测试只读取 `MUJIAN_RECHARGE_TEST_MYSQL` 和 `MUJIAN_RECHARGE_TEST_POSTGRES`，必须提供专用的空测试数据库；测试先检查 users/top_ups 不存在，再创建并在结束时移除这两个测试表，禁止配置生产 DSN。

浏览器验收：先在 web 执行 `bun run build`，再在仓库根目录执行：

```sh
MUJIAN_RECHARGE_BROWSER_FIXTURE=1 go test ./router -run '^TestAdminRechargeBrowserFixture$' -v -count=1
```

该测试在 `http://127.0.0.1:4188` 提供页面和真实 API，数据库位于测试临时目录；不会读取生产数据库 DSN。测试账号为 `recharge-admin`、`recharge-recipient`、`recharge-root`，密码均为仅供本地夹具使用的 `recharge-fixture-password`。用 `POST /__recharge_fixture/finish` 结束，或者等待 15 分钟超时；不启用环境变量时常规测试跳过常驻夹具。

## Artifact

截图与验证日志位于 `web/output/playwright/`，主要截图：

- `admin-recharge-desktop.png`
- `admin-recharge-mobile.png`
- `admin-recharge-history.png`
- `admin-recharge-recipient-wallet.png`
- `admin-recharge-recipient-order.png`

未执行生产部署或生产数据迁移；本地验证没有对真实客户账号加款。数据库最低支持版本 MySQL 5.7/PostgreSQL 9.6 未单独运行，本次实测版本见上。生产迁移会扩大余额字段类型，应按现有发布流程安排。构建保留原有大包/依赖提示，race 链接器有 macOS 符号表提示，但检查均成功退出。
