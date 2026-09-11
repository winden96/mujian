# 羽宇 AI 中转接入

## 当前状态

2026-09-11，本地 `one-api.db` 已配置用户提供的 Key，导入官方价格、同步、测试并启用对话渠道。没有部署到生产。其他 10 条渠道保持不变。

- 供应商：`yuyu`；Base URL：`https://api.yu-yu.ai`，OpenAI 适配器追加 `/v1`。
- 模型发现：`GET /v1/models` 使用推理 Key，最终检查返回 38 个 ID；与精选目录、价格及接口权限取交集后开放 5 个。
- 对话渠道 ID 11，路由分组 `default`，优先级 50；Nano 与 GPT Image 渠道 ID 12、13 保持禁用。
- 密钥仅保存在本地渠道配置，不写入代码、文档或价格文件。

| 公开模型 ID | 上游 ID | 输入 USD / 百万 Token | 输出 USD / 百万 Token |
| --- | --- | ---: | ---: |
| `deepseek-v4-flash` | 同名 | 0.15 | 0.30 |
| `claude-haiku-4-5` | 同名 | 1 | 5 |
| `claude-sonnet-5` | 同名 | 2 | 10 |
| `claude-opus-5` | 同名 | 5 | 25 |
| `claude-fable-5-nc` | `claude-fable-5` | 10 | 50 |

以上为本次官方快照及账户上游分组下的价格；本地用户分组倍率仍由现有计费配置决定。Haiku、Sonnet、DeepSeek 做过真实生成验证，Opus 与 Fable 完成模型权限和价格验证，未逐个付费调用。

## 价格来源与更新

[官方价格页](https://api.yu-yu.ai/pricing) 的 `/api/pricing` 需要账户登录会话，推理 API Key 不能读取。系统使用 Root 导入官方 JSON 的方式，不保存浏览器登录凭据，也不发送推理 Key 尝试登录接口。

本次导入完整响应 39 条，官方 `pricing_version` 为 `a42d372ccf0b5dd13ecf71203521f9d2`。原始记录、分组倍率和完整响应存储在内部 Option `_mujian_yuyu_pricing`，渠道来源版本为导入内容的 SHA-256。快照绑定当前 Key 的摘要，换 Key 后必须重新导入。

更新步骤：

1. 登录羽宇并打开价格页，在浏览器开发者工具 Network 中保存 `/api/pricing` 的完整响应为 JSON 文件，不能仅保存 `data` 数组。
2. Root 进入「渠道管理 → 创作供应商接入 → 羽宇 AI」，点击「导入羽宇价格」。
3. 填羽宇后台该令牌的上游分组；本次令牌为自动跨分组，填写 `auto`。这里不同于本地的路由分组 `default`。
4. 上传文件（最大 2 MB），导入后羽宇路由立即暂停。依次点击「同步模型与价格」「鉴权测试」和启用开关。

同步会重新查询上游可访问模型并使用已导入的价格快照；它不会自动抓取新的账户价格。上游调价、调整令牌分组或换 Key 后，应重新导入并同步。自动分组中同一模型存在不同倍率时拒绝开放，需固定上游分组后重新导入。

导入接口为 `PUT /api/mujian/admin/providers/yuyu/pricing`，受现有 Root 鉴权、关键操作限流及禁用缓存中间件保护，请求体为：

```json
{
  "upstream_group": "auto",
  "pricing": { "success": true, "pricing_version": "官方响应中的版本", "data": [], "group_ratio": {}, "auto_groups": [] }
}
```

示例仅说明结构；须上传完整有效响应。导入、同步和换 Key 复用托管供应商锁与生命周期版本，旧的并发同步结果不能重新启用失效报价。

## 计费适配与边界

普通 Token 模型按 `model_ratio × 2 × 上游分组倍率` 得到百万 Token 输入价，再乘 `completion_ratio` 得到输出价。Claude 的 `tier("base", p * ... + c * ... + cr * ... + cc * ... + cc1h * ...)` 按表达式系数解析输入、输出、缓存读取和写入价格，覆盖过时的 `model_ratio`，不执行表达式代码。仅允许明确的线性变量和运算，一小时缓存价须兼容当前账单协议。

GPT-5.6 系列包含长上下文条件阶梯，GPT Image 包含图像 Token 变量，当前结算模型不能准确表达，因此保持不可用并显示原因。Gemini 图像接口与当前图像路由协议不匹配，也未开放。视频、音乐、TTS 不在本次接入范围。

真实流式 Sonnet 测试中，上游将一句短提示计为 9,236 输入 Token、4 输出 Token，扣费 9,256 quota；本地请求预授权为 2,880 quota，按现有上限仅结算 2,880，并在日志标记「上游用量超出请求价格预授权，结算已限制在授权额度内」。该差额尚不能确定上游原因，应向羽宇核对；没有放宽用户扣费上限，也不声称该次账单一致。用于转售时需评估此类上游额外用量造成的成本差额。

## Artifact：实现与验收

- **Planner**：复用现有供应商、精选目录及计费体系，解决价格接口授权差异和 Claude 表达式定价；以管理 UI、真实生成及账单核对验收。
- **Generator**：增加官方价格导入 API/UI、快照与分组校验、线性计费解析、托管生命周期校验、羽宇 Bearer 鉴权发送器及回归测试。无数据库结构迁移。
- **Evaluator**：按 `code-simplify` → `code-review` → 自测顺序执行；真实测试发现鉴权发送器遗漏后补齐，并重新执行相同检查顺序。
- 服务层、价格结算辅助层回归通过：`go test ./service/mujianprovider ./relay/helper ./relay/common`。
- 相关控制器和鉴权回归通过：`go test ./controller -run 'Claude|YuYu|MujianProvider|ManagedChannel'`；`go test ./relay/channel/claude -run 'TestSetupRequestHeader|TestDoRequest|TestManaged'`；`go test ./relay/channel -run 'YuYu|Managed|Claude|MujianProvider'`。
- 修改范围 ESLint、Prettier、前端构建及 Go 构建通过。前端存在既有大 chunk 提示。
- 隔离 SQLite 实例真实 UI：Root 登录 → 上传官方 JSON → 同步 → 鉴权测试 → 启用，四个写操作均返回 HTTP 200，UI 显示 5 个可用模型、对话启用、图像禁用；使用日志页面显示真实请求和扣费。截图位于 `output/playwright/yuyu-adapted.png`、`output/playwright/yuyu-usage.png`。
- 网关非流式 DeepSeek 返回 `OK`，输入 7、输出 26 Token，本地及上游均扣 4 quota；Claude Haiku 返回 `OK`，输入 10、输出 4 Token，两侧均扣 15 quota。
- 网关流式 DeepSeek 和 Sonnet 均返回 `OK` 与终止标记；Sonnet 的预授权封顶差异见上节。
- 上游错误退款：DeepSeek 的无效 temperature 返回 HTTP 400，用户钱包和 API 令牌余额前后完全一致，没有成功消费记录。
- 现有测试失败：middleware 的 `TestOrdinarySpecificChannelResolvesConcreteAutoGroupFromDB` 缺少分组列引用初始化，SQLite 生成空列名；Claude 的 `TestClaudeManagedRejectedTerminalEvidenceIsNotCommitted` 断言失败、`TestClaudeWebSearchUsageRecordingIsSymmetric` 空指针。使用修改前文件 overlay 复测仍失败，未改动这些既有问题。此前 controller 全包中的 `TestMujianImageGenerationMultipartAPIFlow` 也有既有可用性 fixture 失败；本轮相关对话控制器测试通过，不宣称全库全绿。
- **Artifact / 交接**：本地已启用 5 个对话模型；生产未部署，未验证 Opus/Fable 真实生成或 GPT/图像计费。价格更新需重新导入；停用可直接关闭羽宇启用开关。

## Artifact：生产发布准备（2026-09-11）

- **Planner**：用户已授权部署生产。确认线上地址 `http://1.92.114.137:10088`，当前版本 `v0.12.14-obs.4`，对应公开 `main` 提交 `3ce547a4dda812cbad43428c5f119a3fa63993b9`。HTTPS 仍不可达。
- **Generator**：建立独立发布候选；羽宇依赖的托管渠道定价/结算基础代码尚未发布，因此候选包含这些依赖。没有复制本地数据库、API Key 或运行时环境文件。现有工作目录保留，尚未推送公开源码或操作生产容器。
- **Evaluator**：全量回归首次有 8 项失败；完成 `code-simplify`、`code-review` 后修复了依赖全局列名初始化的两处查询和缺少渠道元数据的空指针。测试补齐图像分组能力、用户唯一标识、流式预授权；修正过时的 Token 估计断言，并让重定向策略单测不再依赖不存在的 DNS 域名。`go test ./...` 和 `go vet ./model ./middleware ./controller ./service ./relay/channel/claude` 现均通过。上述历史失败记录保留作追溯，已不再阻塞发布候选。Gitleaks 对候选暂存改动扫描通过。
- **Artifact / 阻塞**：生产 `root@1.92.114.137` 返回 `Permission denied (publickey,password)`。本机 SSH agent 没有可用身份；旧部署截图文件已不存在。已向用户请求当前可用登录方式。线上保持原版本，生产数据库备份、镜像构建/切换和生产真实调用尚未执行，不能将本地验证称为生产部署成功。
- **后续步骤**：登录后先记录当前容器镜像、Compose 覆盖文件和渠道配置，并创建数据库备份；发布候选源码与不可变版本，构建并校验镜像，复用现有公网端口覆盖；将羽宇 Key 与官方价格通过受保护管理路径配置到生产，同步/测试/启用后做真实生成、失败退款、健康和重启验证。保持其他渠道、用户余额、默认模型、DNS、安全组和 OBS 配置不变。

## Artifact：按羽宇实际用量结算（2026-09-11）

- **Planner**：消除羽宇上游消费与本地预扣封顶造成的差额；按已验证价格快照及实际输入、输出、缓存用量结算。
- **Generator**：仅对羽宇托管渠道将预扣金额作为估计，不作为最终账单上限。实际用量超过预扣时，在同一事务补扣资金来源、令牌并更新幂等账本；余额不足则记录欠额，下一笔请求仍必须通过余额预检。订阅同样记录实际已用额度。其他渠道的现有上限保持不变，不追扣历史测试请求。
- **Evaluator**：按 code-simplify → code-review → 自测执行。回归重放真实 Sonnet（7912 quota）与 Opus（8853 quota）账单，覆盖原生 Claude/归一化 OpenAI 用量、缓存价格、低余额补扣、其他渠道上限、重复结算、事务失败回滚、退款与订阅超额。
- **Artifact**：变更无需数据库迁移。部署版本、完整回归结果及生产逐笔对账证据由本次发布验收报告记录。公网 HTTPS 登录属于独立未解决项。
