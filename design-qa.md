# 幕间 AI · Tabcode 风格全站设计验收

## 验收范围

- 视觉目标：用户提供的 Tabcode 方向与已确认首页样式。
- 页面范围：39 个路由，包含公开页、认证页、普通控制台、Workspace 全画布和系统状态页。
- 角色：guest、user、admin、root。
- 视口：1440×900、768×1024、390×844。
- 状态：loading、empty、populated、error、0 模型与有模型。

## 对比证据

- 首页 / About 同视口并排：`web/output/playwright/design-qa-home-about-1440.png`
- 核心路由快照：`web/e2e/visual.e2e.js-snapshots/`
- Root 设置页：`setting-desktop-darwin.png`、`setting-tablet-darwin.png`、`setting-mobile-darwin.png`

## 评估

### Design Quality

- 全站使用统一的浅色语义令牌，白色表面、黑白操作、弱边框和少量淡彩光晕一致。
- 营销页、产品页、工作台与后台分别使用宽松、中等和紧凑密度，没有将营销留白强行套入 Workspace。
- About 与首页在字体层级、圆角、按钮、光晕和留白上形成同一品牌语言。

### Originality

- 保留幕间 AI 的视频制作叙事、Seedance 流程与开源归属，并未复制 Tabcode 的具体品牌素材。
- 关于页主叙事“把复杂的视频创作，变成一条清晰制作线”与现有产品价值一致。

### Craft

- Header、Sidebar、PageLayout、Surface、Toolbar、EmptyState、表单与 Modal 已统一。
- Root 设置页不再是空白壳层，三档快照均显示真实表单；非 Root 访问明确进入 403。
- 交互动效受 `prefers-reduced-motion` 约束，没有恢复暗色主题或紫色玻璃覆盖层。

### Functionality

- Playwright：326/326 通过，其中 axe 覆盖 39/39 路由，36/36 核心视觉快照通过。
- 可访问性：无 serious/critical axe 问题；导航、抽屉、Modal、Escape、焦点恢复和 200% CSS 缩放主链可用。
- 静态与构建：修改范围 Prettier、全量 ESLint、10/10 Bun 单测、Go controller/router 测试、`git diff --check` 和生产构建全部通过。

## 剩余风险

- tabcode.cc 实时页面在自动浏览器中捕获超时；本轮以用户给定方向、现有截图和已确认首页作为同视口参考。
- 外部 Chat / iframe 只能保证加载状态与外壳，内部 DOM 与样式不在本应用控制范围。
- 视觉快照为 Darwin/Chromium 基线；其他操作系统的字体抗锯齿可能有少量像素差异。
- 全仓 Prettier 仍受 117 个历史/生成文件基线影响；本次修改范围已全部通过。
- 构建仍报告既有 browserslist 数据过期、Lottie `eval` 和大 chunk 警告，未影响本次构建成功。
- 未发布生产环境。

passed
