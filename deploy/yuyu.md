# 羽宇 AI 中转接入

## 当前状态

生产基线为 `v0.12.14-obs.9`，羽宇对话按上游未取整成本 ×1.25 结算，沿用用户组优惠；历史账单不追扣。下方 obs.5–obs.7 记录为历史验收，现行规则参见 `sales-pricing.md`。Seedance 2.5 不在本次发布范围。公网 HTTPS 问题独立保留。

## 本地配置历史

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

## Artifact：生产已切换，最终验收待恢复（2026-09-11）

- **Planner**：按用户提供的 SSH 密码登录已有生产主机，保留公网 10088、PostgreSQL、Redis、OBS 与原账号配置。
- **Generator**：生产运行 `v0.12.14-obs.5`，提交 `111900b13d735dcc20d67b8683adca96e9642d22`，镜像 `sha256:7d5e4de10e974060d5c5c198c8965c23b0644e2ea61d7c39aa659d3171a7d445`。公开 main 与不可变 tag 经匿名 Git 读取核对一致。Docker Hub 连接超时，沿用上一版离线发布方式；前端从该提交带正确版本/源码链接构建，Go 1.26.1 交叉编译 Linux amd64，复用上一版不可变运行镜像并覆盖两个程序。
- 数据库备份：`/opt/mujian/backups/yuyu-20260911/before.dump`；SHA-256 `6a710c611c99630cb1d9ea5549eb48e430069052c75a19f73436b29e34f16586`。回滚环境和原 Compose 端口覆盖亦保存在同目录。不要未经评估恢复整库，以免覆盖上线后的数据。
- **Evaluator**：发布脚本经过 code-simplify、code-review；新前端构建、Linux 程序版本、镜像用户/标签、Compose 配置、容器健康、内外网 `/api/status` 均通过。管理接口完成配置、导入价格、同步（5 模型）、鉴权测试（38 个上游模型）及启用。上线前生产渠道为 0。
- 生产真实生成尚未执行：第一次验收已完成临时用户密码登录，但 Python CookieJar 不会在 HTTP 上发送生产 Secure Cookie，创建验收令牌返回 401。脚本 finally 已正常执行撤销 Root 临时 access_token、禁用临时用户、清零验收额度并删除私有上游 payload。
- 修订后的验收脚本使用服务器 loopback 显式携带登录 Cookie；未修改产品 Cookie 安全策略。但脚本重新传输时 SSH ControlMaster 断开，随后 SSH 在认证前被 reset，传输与复测均未执行。当前没有运行中的生产验收程序。
- **已发现的剩余问题**：公网仅 HTTP，生产 Secure Cookie 导致浏览器登录不可用；HTTPS 尚未恢复。公共 `/pricing` 使用旧全局 ModelRatio，DeepSeek 返回默认 37.5，且普通访客积分数被套用美元符号，页面出现 `$5475 / 1M Tokens`，不代表托管羽宇渠道的实际导入价格。需要将公开报价展示与托管报价来源对齐；尚未修改或验证该独立展示路径。
- **Artifact / 交接**：线上版本及羽宇渠道已启用，但不能宣称完整生产验收成功。恢复 SSH 或云控制台访问后，先核验临时凭据清理、原用户哈希与渠道价格，再运行 `/tmp/mujian-yuyu-adapt/production-configure.py --smoke-only`（脚本须先安全传输至服务器原 build-artifacts 目录），验证 DeepSeek 非流式/流式、Haiku、400 失败退款与重启后状态。随后处理公开报价展示及 HTTPS 登录问题。

## Artifact：继续修复公开报价（2026-09-11）

- **Planner / Generator**：访客复用已有 `/api/mujian/models` 和精选目录 UI；登录普通用户继续读取 `/api/mujian/preferences`，个人默认模型控件只向登录用户展示。仅改 `web/src/pages/Pricing/index.jsx`，没有修改报价数据或结算。提交 `1ddb9c1c50ce86806d99f9c3baefbb25976e2a0d`，分支 `release/yuyu-pricing-20260911`；尚未部署。
- **Evaluator**：完成 code-simplify → code-review；ESLint、Prettier、前端构建通过。构建产物通过本地只读代理连接真实生产目录，浏览器展示 5 个可用模型，DeepSeek 输入 `$0.15`、输出 `$0.30`，断言没有 `$5475` 或个人默认模型控件。截图 `output/playwright/yuyu-public-pricing-fixed.png`。登录后的生产 UI 验证仍未执行，原因是 HTTPS/SSH 阻塞。
- **Artifact / 阻塞**：线上仍为 `obs.5`。SSH TCP 建连后在认证前 reset 或 banner 超时；没有新证据证明是密码错误。华为云控制台已通过 Kimi 打开，但停留在账号登录页，已请求用户完成云账号登录。域名 HTTP 返回 403、HTTPS 握手失败。报价修复后续发布、生产真实生成、失败退款、原用户哈希核对和 HTTPS 修复仍待恢复服务器访问。

## Artifact：原账号生产验收与新账号切换（2026-09-11）

- **Planner**：真实验收生产；用户随后要求换用另一个羽宇账号的 Key。新账号切换是当前任务，以下结果仅适用于切换前原 Key。
- **Generator**：已发布 obs.6，源码 `1ddb9c1c50ce86806d99f9c3baefbb25976e2a0d`，镜像 `sha256:7fd57ab674701b0c28cca17cab7e000bd5928fb0fe8da67f728f6b14acddeafe`；公开价格已修正。
- **Evaluator**：代码先完成 code-simplify、code-review，再通过 lint/build。生产 DeepSeek、Haiku、Sonnet、Opus、Fable 均返回 OK；DeepSeek 流式 DONE、400 失败退款通过。用户/令牌扣费与日志一致。普通临时账号通过 SSH 加密隧道浏览器登录生产、创建项目、发送指令，Opus 回复“验收通过”，刷新后仍存在，输入566/输出25、扣1728 quota。初次临时额度不足返回403且未扣款，提高临时额度后成功。登录及公网匿名价格页均正确显示 DeepSeek $0.15/$0.30。
- **Artifact**：测试账号4–7已禁用、清空测试额度和临时凭据，活动测试 Token 为0；原账号1–3哈希保持 `bab794a0338cd62ce6e323d971d2108f`。原始接口报告保存在 obs.6 本地发布产物目录；工作台截图 `output/playwright/yuyu-production-workspace-verified.png`。公网HTTPS登录未通过，未关闭Secure Cookie；图像/GPT复杂计价模型仍未开放。历史 Sonnet 上游异常用量差额仍需核对。下一步登录用户指定羽宇账号获取Key，重新导入该账号价格、同步、鉴权、生成及退款验证。

## Artifact：新账号已切换并实测（2026-09-11）

- **Planner**：按用户要求切换羽宇账号（186****1460），换Key后重新验收。
- **Generator**：新账号创建“幕间生产”Key（上游账号81、Token112），自动跨分组、启用；重新导入该账号39条价格、同步5模型、鉴权38模型并启用。生产Key与新Key摘要比较一致。应用仍为obs.6，无额外源码变更。切换前备份 `/opt/mujian/backups/yuyu-account-switch-20260911/before.dump`，SHA256 `8be65eeb4697650d95e6bc775cd44607bb68ad7ab8d2db8f9600607dc4cba5f4`。
- **Evaluator**：脚本按code-simplify → code-review →语法和真实调用检查。全部5模型返回OK，DeepSeek SSE返回正文和DONE，HTTP400失败无扣款。首次32Token流式探针无最终正文；提高到256后通过。工作台通过SSH加密隧道以普通新建验收账号登录、创建项目、发消息，Opus回复“新账号验收通过”；刷新仍存在。输入569、输出28、本地与羽宇均扣1773 quota。公网匿名价格页再次验证为DeepSeek输入$0.15/输出$0.30。
- **账单未通过项**：真实Sonnet上游7912、本地2408（差5504），Opus上游8853、本地5980（差2873）quota。上游报告额外输入/缓存Token，超出预授权，本地维持现有封顶；额外用量来源未确认，不能声称全部上游账单一致。没有放宽扣费上限。
- **Artifact**：本轮临时账号8–10及此前4–7均禁用、清零测试额度，活动测试Token为0，Root临时access_token已撤销；原账号1–3哈希仍为 `bab794a0338cd62ce6e323d971d2108f`。本轮私密传输文件已清理；上游生产Key保留供服务调用。验收报告 `../mujian-release-artifacts/yuyu-account-switch-20260911/acceptance.json`，截图 `output/playwright/yuyu-new-account-production-verified.png`。公网HTTPS登录仍未通过，图像和复杂GPT计价模型保持未开放。后续需要恢复HTTPS并核对Claude上游额外用量成本。

## Artifact：实际用量结算已部署并对账（2026-09-11）

- **Planner / Generator**：差额源于最终结算截断到预扣上限。羽宇按已验证价格快照和上游实际Token/缓存用量结算；预扣是估计，实际超过余额也完整记账，欠额阻止后续请求。钱包、Token、订阅和幂等账本仍在同一事务更新；其他渠道规则保持原样。无数据库迁移、无额外兜底、不追扣历史请求。
- **Evaluator**：code-simplify、code-review、定向回归、全量 `go test ./...`、相关 `go vet`、前端构建、Gitleaks 均通过。回归重放历史7912/8853账单，覆盖两种用量语义、缓存、欠额、幂等、回滚和退款。
- 生产 `v0.12.14-obs.7`，源码 `de977a568f64063287ac0b79c6b76840a7b8d8a0`，镜像 `sha256:daae2f39e9c30f1f8804e263f539fb16acd241831825054a3fc3c3844f5fa134`。公开main和不可变tag一致；沿用离线镜像构建方式。回滚环境及数据库备份位于 `/opt/mujian/backups/yuyu-actual-billing-20260911`。
- 真实上游/本地钱包/Token/日志逐笔相等：DeepSeek非流式3、流式4、Haiku15、Sonnet9268、Opus7345、Fable170 quota；上游400失败全部0。Sonnet原预扣2408、Opus原预扣5980，本次均正确补扣到实际金额。
- 真实工作台：第一次“只回复”测试缺少Agent正文标记，产品未接受，上游已完成消费，本地及羽宇均10923 quota；正常创作请求成功返回雨夜便利店开场白并在刷新后保留，两侧均2405 quota。使用日志两行显示正确，总钱包减少13328 quota。该格式可靠性问题独立记录，不宣称本次修复。
- **Artifact**：临时账号11–12及Token已禁用，预扣无未结算记录；原用户1–3数据未变。完整证据 `../mujian-release-artifacts/yuyu-actual-billing-20260911/acceptance.json`；截图 `output/playwright/yuyu-actual-billing-workspace.png`、`output/playwright/yuyu-actual-billing-logs.png`。公共HTTPS仍不可用，浏览器通过SSH隧道访问真实生产。上游调价仍需重新导入快照。

## Sales pricing update

The [managed sales pricing policy](sales-pricing.md) supersedes the previous
1:1 YuYu settlement: all managed relays charge upstream cost × 1.25.
