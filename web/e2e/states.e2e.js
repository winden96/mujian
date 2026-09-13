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
import { visitRoute, waitForMockIdle } from './support/mock-api';
import { expect, test } from './support/test-fixtures';

test('projects exposes a recoverable loading and empty state', async ({
  page,
}) => {
  await visitRoute(page, routeById('projects'), {
    role: 'user',
    waitForIdle: false,
    scenario: {
      projects: 'empty',
      delay: { '/api/mujian/projects': 350 },
    },
  });

  await expect(page.locator('.semi-spin')).toBeVisible();
  await waitForMockIdle(page);
  await expect(page.getByText('还没有项目，先创建一个故事。')).toBeVisible();
  await expect(page.getByRole('button', { name: '新建项目' })).toBeVisible();
});

test('projects populated state links into its workspace', async ({ page }) => {
  await visitRoute(page, routeById('projects'), { role: 'user' });

  await expect(page.getByRole('heading', { name: '雨夜便利店' })).toBeVisible();
  await expect(
    page.getByRole('link', { name: '打开项目：雨夜便利店' }),
  ).toHaveAttribute('href', '/console/mujian/projects/e2e-project/workspace');
});

test('normal user sees an explanatory zero-model state', async ({ page }) => {
  await visitRoute(page, routeById('pricing'), {
    role: 'user',
    scenario: { models: 'empty' },
  });

  await expect(page.getByText(/模型目录.*配置/)).toBeVisible();
  await expect(page.getByText('等待渠道配置')).toBeVisible();
  await expect(page.getByText('0 个可用模型')).toBeVisible();
});

test('normal user sees available and pending catalog models', async ({
  page,
}) => {
  await visitRoute(page, routeById('pricing'), { role: 'user' });

  await expect(
    page.getByRole('heading', { name: 'GPT-4o mini' }),
  ).toBeVisible();
  await expect(
    page.getByRole('heading', { name: 'Seedance Image Preview' }),
  ).toBeVisible();
});

for (const role of ['guest', 'admin']) {
  test(`${role} pricing keeps the legacy pricing branch`, async ({ page }) => {
    await visitRoute(page, routeById('pricing'), { role });

    await expect(page.locator('.pricing-layout')).toBeVisible();
    await expect(page.getByRole('heading', { name: '模型广场' })).toHaveCount(
      0,
    );
    await expect(page.getByLabel('搜索模型')).toHaveCount(0);
  });
}

test('about API failure keeps the branded page and a retry action', async ({
  page,
}) => {
  await visitRoute(page, routeById('about'), {
    role: 'guest',
    scenario: { about: 'error' },
  });

  await expect(
    page.getByRole('heading', { name: /把复杂的视频创作/ }),
  ).toBeVisible();
  await expect(
    page.getByRole('alert').filter({ hasText: '默认品牌页' }).first(),
  ).toBeVisible();
  await expect(page.getByRole('button', { name: '重试' })).toBeVisible();
});

test('about configured content takes priority over the default brand page', async ({
  page,
}) => {
  await visitRoute(page, routeById('about'), {
    role: 'guest',
    scenario: { about: 'custom' },
  });

  await expect(
    page.getByRole('heading', { name: '自定义关于页' }),
  ).toBeVisible();
  await expect(page.getByText('E2E 可配置内容。')).toBeVisible();
  await expect(page.getByText('把复杂的视频创作')).toHaveCount(0);
});

test('system settings rejects a non-root administrator', async ({ page }) => {
  await visitRoute(page, routeById('setting'), { role: 'admin' });

  await expect.poll(() => new URL(page.url()).pathname).toBe('/forbidden');
});

test('root system settings renders content and repairs an invalid tab', async ({
  page,
}) => {
  const route = {
    ...routeById('setting'),
    path: '/console/setting?tab=invalid',
  };
  const { audit, pageErrors } = await visitRoute(page, route, { role: 'root' });

  await expect.poll(() => new URL(page.url()).search).toBe('?tab=operation');
  await expect(page.getByRole('tab', { name: '运营设置' })).toBeVisible();
  await expect(
    page.getByText('通用设置', { exact: true }).first(),
  ).toBeVisible();
  expect(audit.requests).toContainEqual({
    body: null,
    method: 'GET',
    pathname: '/api/option/',
  });
  expect(pageErrors, pageErrors.map(String).join('\n')).toEqual([]);
});
