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

import { routeById } from './support/route-manifest';
import {
  getPageAudit,
  installApiMocks,
  visitRoute,
  waitForApp,
  waitForMockIdle,
} from './support/mock-api';
import { expect, test } from './support/test-fixtures';

const docsEnabledStatus = {
  docs_link: 'https://docs.newapi.pro/zh',
  HeaderNavModules: JSON.stringify({
    home: true,
    console: true,
    pricing: { enabled: true, requireAuth: false },
    docs: true,
    about: true,
  }),
};

const openOperation = async (page, methodClass, summary) => {
  const operation = page
    .locator(`.swagger-ui .opblock.${methodClass}`)
    .filter({ hasText: summary })
    .first();
  await operation.locator('.opblock-summary').click();
  return operation;
};

const executeOperation = async (operation) => {
  await operation.locator('.try-out__btn').click();
  await operation.locator('.execute').click();
  await expect(
    operation.locator('.responses-table tbody .response-col_status').first(),
  ).toHaveText('200');
};

test('external docs_link still opens the same-tab /docs route', async ({
  page,
}) => {
  await visitRoute(page, routeById('about'), {
    role: 'guest',
    scenario: { status: docsEnabledStatus },
  });

  const docsLink = page
    .getByRole('navigation', { name: '主导航' })
    .getByRole('link', { name: '文档' });
  await expect(docsLink).toHaveAttribute('href', '/docs');
  await expect(docsLink).not.toHaveAttribute('target', '_blank');

  const pageCount = page.context().pages().length;
  await docsLink.click();

  await expect(page).toHaveURL(/\/docs$/);
  expect(page.context().pages()).toHaveLength(pageCount);
  await expect(docsLink).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('.swagger-ui')).toBeVisible();
  await waitForMockIdle(page);
});

test('HeaderNavModules.docs=false still hides the docs navigation item', async ({
  page,
}) => {
  await visitRoute(page, routeById('about'), { role: 'guest' });

  await expect(
    page
      .getByRole('navigation', { name: '主导航' })
      .getByRole('link', { name: '文档' }),
  ).toHaveCount(0);
});

test('docs support search, copy, manual authorization, and GET/POST execution without cookies', async ({
  context,
  page,
}) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await visitRoute(page, routeById('docs'), {
    role: 'guest',
    scenario: { status: docsEnabledStatus },
  });
  await expect(page.locator('.swagger-ui')).toBeVisible();

  const filter = page.locator('.swagger-ui .operation-filter-input');
  await filter.fill('对话');
  await expect(page.locator('.opblock-post')).toContainText('创建对话补全');
  await filter.clear();

  const copyButton = page.locator('.mujian-docs-copy-code');
  await copyButton.click();
  await expect(copyButton).toContainText('已复制');

  await page
    .getByRole('button', { name: /Authorize/ })
    .first()
    .click();
  const authModal = page.locator('.swagger-ui .dialog-ux');
  await authModal.locator('input[type="text"]').fill('e2e-manual-token');
  await authModal.locator('button.authorize').click();
  await authModal.locator('button.close-modal').click();

  await context.addCookies([
    {
      name: 'console_session',
      value: 'must-not-leave-docs',
      url: new URL(page.url()).origin,
    },
  ]);

  const modelsOperation = await openOperation(
    page,
    'opblock-get',
    '列出可用模型',
  );
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).hash))
    .toBe('#/模型/listModels');
  await executeOperation(modelsOperation);

  const chatOperation = await openOperation(
    page,
    'opblock-post',
    '创建对话补全',
  );
  await chatOperation.locator('.try-out__btn').click();
  await chatOperation.locator('textarea').fill(
    JSON.stringify({
      model: 'gpt-4o-mini',
      messages: [{ role: 'user', content: 'E2E' }],
    }),
  );
  await chatOperation.locator('.execute').click();
  await expect(
    chatOperation
      .locator('.responses-table tbody .response-col_status')
      .first(),
  ).toHaveText('200');
  await waitForMockIdle(page);

  const debugRequests = getPageAudit(page).requests.filter(({ pathname }) =>
    ['/v1/models', '/v1/chat/completions'].includes(pathname),
  );
  expect(debugRequests).toHaveLength(2);
  for (const request of debugRequests) {
    expect(request.headers.authorization).toBe('Bearer e2e-manual-token');
    expect(request.headers.cookie).toBeUndefined();
  }

  await page.reload({ waitUntil: 'domcontentloaded' });
  await waitForApp(page);
  await expect(page.locator('.swagger-ui')).toBeVisible();
  await expect(
    page
      .locator('.swagger-ui .opblock.opblock-post')
      .filter({ hasText: '创建对话补全' })
      .first(),
  ).toHaveClass(/is-open/);
  const modelsAfterReload = await openOperation(
    page,
    'opblock-get',
    '列出可用模型',
  );
  await executeOperation(modelsAfterReload);
  await waitForMockIdle(page);

  const latestModelsRequest = getPageAudit(page)
    .requests.filter(({ pathname }) => pathname === '/v1/models')
    .at(-1);
  expect(latestModelsRequest.headers.authorization).toBeUndefined();
  expect(latestModelsRequest.headers.cookie).toBeUndefined();
});

test('guest personal-config CTA returns to integrations after password login', async ({
  page,
}) => {
  await visitRoute(page, routeById('docs'), { role: 'guest' });

  await page.getByRole('link', { name: '获取个人配置' }).click();
  await expect(page).toHaveURL(
    /\/login\?next=%2Fconsole%2Fmujian%2Fintegrations$/,
  );
  await page.getByLabel('用户名或邮箱').fill('e2e-user');
  await page.getByLabel('密码').fill('e2e-password');
  await page.getByRole('button', { name: '继续' }).click();

  await expect(page).toHaveURL(/\/console\/mujian\/integrations$/);
  await expect(page.getByRole('heading', { name: '客户端接入' })).toBeVisible();
});

test('OAuth callback consumes its state-bound return target once', async ({
  page,
}) => {
  const state = 'e2e-oauth-state';
  const storageKey = `mujian:oauth:return:${state}`;
  await installApiMocks(page, {
    role: 'guest',
    scenario: {
      sessionStorage: {
        [storageKey]: JSON.stringify({
          state,
          target: '/console/mujian/integrations',
        }),
      },
    },
  });

  await page.goto(`/oauth/github?code=e2e-code&state=${state}`, {
    waitUntil: 'domcontentloaded',
  });
  await waitForApp(page);
  await expect(page).toHaveURL(/\/console\/mujian\/integrations$/);
  await expect
    .poll(() => page.evaluate((key) => sessionStorage.getItem(key), storageKey))
    .toBeNull();
});
