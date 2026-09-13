/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import { ROLE_FIXTURES } from './route-manifest';

const TEST_BASE_URL = new URL(
  process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:4179',
);
const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const testHostPattern = escapeRegExp(TEST_BASE_URL.host);
const EXTERNAL_HTTP_PATTERN = new RegExp(
  `^https?:\\/\\/(?!${testHostPattern}(?:\\/|$))`,
  'i',
);
const EXTERNAL_WEB_SOCKET_PATTERN = new RegExp(
  `^wss?:\\/\\/(?!${testHostPattern}(?:\\/|$))`,
  'i',
);
const DOCS_ICON_URL =
  'https://lf3-static.bytednsdoc.com/obj/eden-cn/ptlz_zlp/ljhwZthlaukjlkulzlp/docs-icon.png';
const TRANSPARENT_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M/wHwAF/gL+X2NDVQAAAABJRU5ErkJggg==',
  'base64',
);
const PAGE_AUDITS = new WeakMap();

const success = (data, extra = {}) => ({
  success: true,
  message: '',
  data,
  ...extra,
});

const emptyPage = () => ({
  items: [],
  page: 1,
  page_size: 10,
  total: 0,
  type_counts: {},
  vendor_counts: {},
});

const fullSidebarConfig = {
  chat: { enabled: true, playground: true, chat: true },
  console: {
    enabled: true,
    detail: true,
    token: true,
    log: true,
    midjourney: true,
    task: true,
  },
  personal: { enabled: true, topup: true, personal: true },
  admin: {
    enabled: true,
    channel: true,
    models: true,
    deployment: true,
    redemption: true,
    user: true,
    subscription: true,
    setting: true,
  },
};

export const MOCK_STATUS = Object.freeze({
  setup: true,
  system_name: '幕间 AI E2E',
  logo: '',
  server_address: 'http://127.0.0.1:4179',
  footer_html: '',
  quota_per_unit: 500000,
  display_in_currency: true,
  quota_display_type: 'USD',
  price: 1,
  usd_exchange_rate: 7.2,
  custom_currency_exchange_rate: 1,
  custom_currency_symbol: '¤',
  enable_drawing: true,
  enable_task: true,
  enable_data_export: true,
  data_export_default_time: 'hour',
  default_collapse_sidebar: false,
  registration_enabled: true,
  email_verification: false,
  turnstile_check: false,
  self_use_mode_enabled: false,
  demo_site_enabled: false,
  uptime_enabled: false,
  mj_notify_enabled: false,
  announcements: [],
  faq: [],
  api_info: [],
  chats: [],
  chat_link: '',
  chat_link2: '',
  docs_link: '',
  HeaderNavModules: JSON.stringify({
    home: true,
    console: true,
    pricing: { enabled: true, requireAuth: false },
    docs: false,
    about: true,
  }),
  SidebarModulesAdmin: JSON.stringify(fullSidebarConfig),
});

const MOCK_RELAY_OPENAPI = Object.freeze({
  openapi: '3.0.3',
  info: {
    title: '幕间模型网关 API',
    version: '1.0.0',
  },
  servers: [{ url: TEST_BASE_URL.origin }],
  tags: [{ name: '模型' }, { name: '对话' }],
  paths: {
    '/v1/models': {
      get: {
        tags: ['模型'],
        summary: '列出可用模型',
        operationId: 'listModels',
        security: [{ BearerAuth: [] }],
        responses: {
          200: {
            description: '模型列表',
            content: {
              'application/json': {
                schema: {
                  type: 'object',
                  properties: {
                    object: { type: 'string' },
                    data: {
                      type: 'array',
                      items: { $ref: '#/components/schemas/Model' },
                    },
                  },
                },
              },
            },
          },
        },
      },
    },
    '/v1/chat/completions': {
      post: {
        tags: ['对话'],
        summary: '创建对话补全',
        operationId: 'createChatCompletion',
        security: [{ BearerAuth: [] }],
        requestBody: {
          required: true,
          content: {
            'application/json': {
              schema: {
                type: 'object',
                required: ['model', 'messages'],
                properties: {
                  model: { type: 'string', example: 'gpt-4o-mini' },
                  messages: {
                    type: 'array',
                    items: {
                      type: 'object',
                      required: ['role', 'content'],
                      properties: {
                        role: { type: 'string', example: 'user' },
                        content: { type: 'string', example: '你好' },
                      },
                    },
                  },
                },
              },
            },
          },
        },
        responses: {
          200: { description: '对话结果' },
        },
      },
    },
  },
  components: {
    securitySchemes: {
      BearerAuth: {
        type: 'http',
        scheme: 'bearer',
        bearerFormat: 'API Token',
      },
    },
    schemas: {
      Model: {
        type: 'object',
        required: ['id', 'object'],
        properties: {
          id: { type: 'string' },
          object: { type: 'string' },
        },
      },
    },
  },
});

const SAMPLE_PROJECT = Object.freeze({
  id: 'e2e-project',
  title: '雨夜便利店',
  synopsis: '一位失眠店员在雨夜遇见来自未来的客人。',
  type: '短剧',
  cover_url: '',
  current_episode: 1,
  progress: 36,
  updated_at: 1788000000,
});

const SAMPLE_MODELS = Object.freeze({
  chat: ['gpt-4o-mini'],
  image: ['gpt-image-1'],
  items: [
    {
      id: 'gpt-4o-mini',
      name: 'GPT-4o mini',
      kind: 'chat',
      provider_name: 'OpenAI',
      available: true,
      channel_count: 2,
      tags: ['剧本', '分镜'],
      providers: ['主线路', '备用线路'],
      billing_type: 'token',
      min_input_price: 0.15,
      max_input_price: 0.15,
      min_output_price: 0.6,
      max_output_price: 0.6,
    },
    {
      id: 'seedance-image-preview',
      name: 'Seedance Image Preview',
      kind: 'image',
      provider_name: 'ByteDance',
      available: false,
      channel_count: 0,
      tags: ['角色设定', '分镜图'],
      providers: [],
      billing_type: 'fixed',
      min_fixed_price: 0.02,
      max_fixed_price: 0.02,
      unavailable_reason: '等待渠道配置',
    },
  ],
});

const preferencePayload = (modelState) => ({
  default_chat_model: modelState.chat[0] || '',
  default_image_model: modelState.image[0] || '',
  enabled_skills: JSON.stringify(['短剧编剧', '分镜导演', '画面提示词']),
});

const workspacePayload = {
  project: SAMPLE_PROJECT,
  active_session_id: 'e2e-session',
  agent_sessions: [
    {
      id: 'e2e-session',
      title: '雨夜便利店 · 主会话',
      updated_at: 1788000000,
    },
  ],
  messages: [],
  image_generations: [],
};

const roleUser = (roleName) =>
  Object.prototype.hasOwnProperty.call(ROLE_FIXTURES, roleName)
    ? ROLE_FIXTURES[roleName]
    : ROLE_FIXTURES.user;

const responseFor = ({ method, pathname, roleName, scenario, status }) => {
  const modelState =
    scenario.models === 'empty'
      ? { ...SAMPLE_MODELS, chat: [], image: [], items: [] }
      : SAMPLE_MODELS;
  const user = roleUser(roleName);
  const projects = scenario.projects === 'empty' ? [] : [SAMPLE_PROJECT];

  if (pathname === '/api/status') return success(status);
  if (pathname === '/api/openapi/relay.json') return MOCK_RELAY_OPENAPI;
  if (pathname === '/api/setup') {
    return success({ status: false, root_init: true, database_type: 'sqlite' });
  }
  if (pathname === '/api/about') {
    if (scenario.about === 'custom') {
      return success('# 自定义关于页\n\nE2E 可配置内容。');
    }
    return success('');
  }
  if (
    pathname === '/api/user-agreement' ||
    pathname === '/api/privacy-policy'
  ) {
    return success('');
  }
  if (/^\/api\/oauth\/(github|discord|oidc|linuxdo|custom)$/.test(pathname)) {
    return success(user || ROLE_FIXTURES.user);
  }
  if (pathname === '/api/oauth/state') return success('e2e-oauth-state');
  if (pathname === '/api/user/login' && method === 'POST') {
    return success(ROLE_FIXTURES.user);
  }

  if (pathname === '/api/user/self') return success(user);
  if (pathname === '/api/user/self/groups') {
    return success({ default: { desc: '默认分组', ratio: 1 } });
  }
  if (pathname === '/api/user/models') {
    return success(['gpt-4o-mini', 'gpt-image-1']);
  }
  if (pathname === '/api/user/token') return success('e2e-user-token');
  if (pathname === '/api/user/aff') return success('e2e-affiliate');
  if (pathname === '/api/user/topup/info') {
    return success({
      amount_options: [],
      discount: {},
      pay_methods: [],
      stripe_min_topup: 0,
    });
  }
  if (pathname === '/api/user/amount' && method === 'POST') {
    return { success: true, message: 'success', data: 1 };
  }
  if (pathname === '/api/user/passkey') return success([]);
  if (pathname === '/api/user/2fa/status') return success({ enabled: false });
  if (pathname === '/api/user/oauth/bindings') return success([]);
  if (/^\/api\/user\/\d+$/.test(pathname)) return success(user);
  if (pathname === '/api/user/' || pathname === '/api/user/search') {
    return success(emptyPage());
  }

  if (pathname === '/api/mujian/projects' && method === 'GET') {
    return success(projects);
  }
  if (pathname === '/api/mujian/projects' && method === 'POST') {
    return success(SAMPLE_PROJECT);
  }
  if (/^\/api\/mujian\/projects\/[^/]+\/workspace$/.test(pathname)) {
    return success(workspacePayload);
  }
  if (/^\/api\/mujian\/projects\/[^/]+\/agent\/sessions$/.test(pathname)) {
    return success(workspacePayload);
  }
  if (
    /^\/api\/mujian\/projects\/[^/]+\/agent\/sessions\/[^/]+\/messages$/.test(
      pathname,
    )
  ) {
    return success({ ...workspacePayload, messages: [] });
  }
  if (/^\/api\/mujian\/projects\/[^/]+\/image-generations/.test(pathname)) {
    return success([]);
  }
  if (pathname === '/api/mujian/preferences') {
    return success(preferencePayload(modelState), { models: modelState });
  }
  if (pathname === '/api/mujian/models') return success(modelState);
  if (pathname === '/api/mujian/skills') {
    return success(['短剧编剧', '分镜导演', '画面提示词']);
  }
  if (pathname === '/api/mujian/wallet') {
    return success({
      credits: 1280,
      credits_per_cny: 10,
      packages: [
        { credits: 100, amount: 10 },
        { credits: 500, amount: 48 },
      ],
      custom_amount: { min: 10, max: 10000, step: 10 },
      payment: {
        enabled: false,
        methods: [],
        disabled_reason: 'E2E 测试环境不启用支付',
      },
    });
  }
  if (pathname === '/api/mujian/wallet/orders') return success(emptyPage());
  if (pathname === '/api/mujian/admin/providers') return success([]);

  if (pathname === '/api/pricing') {
    return success([], {
      vendors: [],
      group_ratio: { default: 1 },
      usable_group: { default: '默认分组' },
      supported_endpoint: {},
      auto_groups: [],
    });
  }
  if (pathname === '/api/option/') return success([]);
  if (pathname === '/api/group/' || pathname === '/api/group') {
    return success({ default: { desc: '默认分组', ratio: 1 } });
  }
  if (pathname === '/api/vendors/' || pathname === '/api/vendors') {
    return success(emptyPage());
  }
  if (pathname === '/api/models/missing') return success([]);
  if (pathname === '/api/channel/models') return success([]);
  if (pathname === '/api/models') return success({});
  if (pathname === '/api/models/' || pathname === '/api/models/search') {
    return success(emptyPage());
  }
  if (pathname === '/api/deployments/settings') {
    return success({ enabled: false });
  }
  if (pathname === '/api/deployments/hardware-types') return success([]);
  if (
    pathname === '/api/deployments/' ||
    pathname === '/api/deployments/search'
  ) {
    return success(emptyPage());
  }
  if (pathname === '/api/subscription/admin/plans') return success([]);
  if (pathname === '/api/subscription/plans') return success([]);
  if (pathname === '/api/subscription/self') return success(null);
  if (pathname === '/api/custom-oauth-provider') return success([]);
  if (pathname === '/api/prefill_group') return success([]);

  if (pathname === '/api/channel/' || pathname === '/api/channel/search') {
    return success(emptyPage());
  }
  if (
    pathname === '/api/token/' &&
    ['chat', 'chat2link'].includes(scenario.routeId)
  ) {
    return success({
      ...emptyPage(),
      items: [{ id: 1, name: 'E2E Chat Token', status: 1 }],
      total: 1,
    });
  }
  if (/^\/api\/token\/\d+\/key$/.test(pathname)) {
    return success({ key: 'e2e-chat-key' });
  }
  if (pathname === '/api/token/' || pathname === '/api/token/search') {
    return success(emptyPage());
  }
  if (
    pathname === '/api/redemption/' ||
    pathname === '/api/redemption/search'
  ) {
    return success(emptyPage());
  }
  if (pathname === '/api/log/' || pathname === '/api/log/self/') {
    return success(emptyPage());
  }
  if (pathname === '/api/log/stat' || pathname === '/api/log/self/stat') {
    return success({ quota: 0, count: 0 });
  }
  if (pathname === '/api/mj/' || pathname === '/api/mj/self/') {
    return success(emptyPage());
  }
  if (pathname === '/api/task/' || pathname === '/api/task/self') {
    return success(emptyPage());
  }
  if (pathname === '/api/data/' || pathname === '/api/data/self/') {
    return success([]);
  }
  if (pathname === '/api/data/users' || pathname === '/api/uptime/status') {
    return success([]);
  }
  if (pathname === '/api/performance/stats') {
    return success({ enabled: false, memory: {}, disk: {}, cache: {} });
  }
  if (pathname === '/api/performance/logs') return success([]);

  if (pathname === '/v1/models' && method === 'GET') {
    return {
      object: 'list',
      data: [{ id: 'gpt-4o-mini', object: 'model' }],
    };
  }
  if (pathname === '/v1/chat/completions' && method === 'POST') {
    return {
      id: 'chatcmpl-e2e',
      object: 'chat.completion',
      choices: [
        {
          index: 0,
          message: { role: 'assistant', content: 'E2E 响应' },
          finish_reason: 'stop',
        },
      ],
    };
  }

  return undefined;
};

export const installApiMocks = async (
  page,
  { role = 'guest', scenario = {} } = {},
) => {
  const user = ROLE_FIXTURES[role];
  const status = { ...MOCK_STATUS, ...(scenario.status || {}) };
  const audit = {
    allowedExternalAssets: [],
    issues: [],
    lastActivity: Date.now(),
    pending: 0,
    requests: [],
  };
  PAGE_AUDITS.set(page, audit);

  const blockExternalHttp = async (route) => {
    const url = route.request().url();
    audit.issues.push(`Blocked unexpected external HTTP request: ${url}`);
    await route.abort('blockedbyclient');
  };

  await page.addInitScript(
    ({ seededUser, status, sessionValues }) => {
      localStorage.clear();
      sessionStorage.clear();
      localStorage.setItem('theme-mode', 'dark');
      localStorage.setItem('i18nextLng', 'zh');
      localStorage.setItem('status', JSON.stringify(status));
      localStorage.setItem('system_name', status.system_name);
      localStorage.setItem('enable_data_export', 'true');
      localStorage.setItem('enable_drawing', 'true');
      localStorage.setItem('enable_task', 'true');
      if (seededUser) {
        localStorage.setItem('user', JSON.stringify(seededUser));
      }
      for (const [key, value] of Object.entries(sessionValues)) {
        sessionStorage.setItem(key, value);
      }
      window.__MUJIAN_E2E__ = true;
    },
    { seededUser: user, status, sessionValues: scenario.sessionStorage || {} },
  );

  await page.route(EXTERNAL_HTTP_PATTERN, blockExternalHttp);
  await page.route(DOCS_ICON_URL, async (route) => {
    audit.allowedExternalAssets.push(DOCS_ICON_URL);
    await route.fulfill({
      status: 200,
      contentType: 'image/png',
      body: TRANSPARENT_PNG,
    });
  });
  await page.routeWebSocket(EXTERNAL_WEB_SOCKET_PATTERN, async (webSocket) => {
    audit.issues.push(
      `Blocked unexpected external WebSocket request: ${webSocket.url()}`,
    );
    await webSocket.close({ code: 1008, reason: 'E2E network isolation' });
  });

  const handler = async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== TEST_BASE_URL.origin) {
      await blockExternalHttp(route);
      return;
    }
    const method = request.method();
    const bodyText = request.postData();
    let body = bodyText;
    if (bodyText) {
      try {
        body = JSON.parse(bodyText);
      } catch {
        // Preserve non-JSON bodies verbatim in the request audit.
      }
    }
    audit.pending += 1;
    audit.lastActivity = Date.now();
    audit.requests.push({
      body,
      headers: await request.allHeaders(),
      method,
      pathname: url.pathname,
    });

    try {
      if (scenario.delay?.[url.pathname]) {
        await new Promise((resolve) =>
          setTimeout(resolve, scenario.delay[url.pathname]),
        );
      }

      if (scenario.about === 'error' && url.pathname === '/api/about') {
        await route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: JSON.stringify({ success: false, message: 'E2E 网络异常' }),
        });
        return;
      }

      if (
        method === 'POST' &&
        /^\/api\/mujian\/projects\/[^/]+\/agent\/messages\/stream$/.test(
          url.pathname,
        )
      ) {
        const createdAt = 1788000100;
        const sessionId = body?.session_id || 'e2e-session';
        const projectId = url.pathname.split('/')[4];
        const stream = [
          `event: started\ndata: ${JSON.stringify({ session_id: sessionId })}\n\n`,
          `event: delta\ndata: ${JSON.stringify({ session_id: sessionId, text: '已生成开场钩子。' })}\n\n`,
          `event: completed\ndata: ${JSON.stringify({
            session_id: sessionId,
            user_message: {
              id: 'e2e-user-message',
              project_id: projectId,
              session_id: sessionId,
              role: 'user',
              content: body?.content || '',
              created_at: createdAt,
            },
            message: {
              id: 'e2e-assistant-message',
              project_id: projectId,
              session_id: sessionId,
              role: 'assistant',
              content: '已生成开场钩子。',
              created_at: createdAt + 1,
            },
          })}\n\n`,
        ].join('');
        await route.fulfill({
          status: 200,
          contentType: 'text/event-stream; charset=utf-8',
          body: stream,
        });
        return;
      }

      const response = responseFor({
        method,
        pathname: url.pathname,
        roleName: role,
        scenario,
        status,
      });
      if (response === undefined) {
        const issue = `Unregistered E2E API request: ${method} ${url.pathname}`;
        audit.issues.push(issue);
        await route.fulfill({
          status: 501,
          contentType: 'application/json',
          body: JSON.stringify({ success: false, message: issue }),
        });
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(response),
      });
    } finally {
      audit.pending -= 1;
      audit.lastActivity = Date.now();
    }
  };

  await page.route('**/api/**', handler);
  await page.route('**/mj/**', handler);
  await page.route('**/pg/**', handler);
  await page.route('**/v1/**', handler);
};

export const waitForApp = async (page) => {
  await page.locator('#root [data-layout-mode]').first().waitFor();
};

export const waitForMockIdle = async (page, timeout = 7500) => {
  const audit = PAGE_AUDITS.get(page);
  if (!audit) throw new Error('API mocks must be installed before waiting');
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (audit.pending === 0 && Date.now() - audit.lastActivity >= 75) return;
    await page.waitForTimeout(25);
  }
  throw new Error(`Mock API did not become idle (${audit.pending} pending)`);
};

export const getPageAudit = (page) => PAGE_AUDITS.get(page);

export const visitRoute = async (
  page,
  route,
  { waitForIdle = true, ...options } = {},
) => {
  const pageErrors = [];
  page.on('pageerror', (error) => pageErrors.push(error));
  await installApiMocks(page, {
    ...options,
    scenario: { ...options?.scenario, routeId: route.id },
  });
  await page.goto(route.path, { waitUntil: 'domcontentloaded' });
  await waitForApp(page);
  if (waitForIdle) {
    await page.waitForLoadState('networkidle');
    await waitForMockIdle(page);
  }
  return { audit: getPageAudit(page), pageErrors };
};
