# 幕间 AI 浏览器验收

这套测试只接受 loopback 地址上的 Vite 页面。浏览器会在请求发出前拦截 `/api/**`、`/v1/**`、`/mj/**` 和 `/pg/**`，未登记的 API 会返回 501 并使测试失败。非当前 Vite 源的 HTTP(S) 和 WebSocket 也会被阻断，Service Worker 禁用，因此不会创建真实项目、修改渠道、发起支付或调用付费模型。

已登记的唯一外部静态资源是操练场的 `lf3-static.bytednsdoc.com/.../docs-icon.png`；测试用本地 1×1 透明 PNG 替代并记录该请求，不会访问字节网络。

```bash
bun run test:e2e:smoke
bun run test:e2e
bun run test:e2e:update
```

默认由 Playwright 在专用端口 `127.0.0.1:4179` 启动 Vite 服务。优先使用当前 Playwright 版本的捆绑 Chromium；本机未安装该浏览器时，回退到系统 Google Chrome。该端口如果已占用会直接失败，避免误测另一个本地应用。也可以连接已经启动的幕间 AI 页面：

```bash
PLAYWRIGHT_BASE_URL=http://127.0.0.1:3101 \
PLAYWRIGHT_SKIP_WEBSERVER=1 \
bun run test:e2e:smoke
```

HTML 报告、失败截图、trace 和视频统一写入 `output/playwright/`。
视觉基线目前为 macOS Darwin 基线；在其他操作系统运行视觉测试前，需先在目标环境审核并生成对应基线。
