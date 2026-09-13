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

import { routeById, VIEWPORTS } from './support/route-manifest';
import { getPageAudit, visitRoute, waitForMockIdle } from './support/mock-api';
import { expect, test } from './support/test-fixtures';

test('project creation enters Workspace and renders a mocked Agent reply', async ({
  page,
}) => {
  await visitRoute(page, routeById('projects'), {
    role: 'user',
    scenario: { projects: 'empty' },
  });

  await page.getByRole('button', { name: '新建项目' }).click();
  await page.getByLabel('项目名称').fill('雨夜便利店');
  await page
    .getByLabel('故事梗概')
    .fill('一位失眠店员在雨夜遇见来自未来的客人。');
  const createButton = page
    .getByRole('dialog')
    .getByRole('button', { name: 'confirm' });
  await expect(createButton).toContainText('创建并进入');
  await createButton.click();

  await expect(page).toHaveURL(
    /\/console\/mujian\/projects\/e2e-project\/workspace/,
  );
  await expect(
    page
      .getByRole('region', { name: '创作 Agent' })
      .getByText('雨夜便利店', { exact: true }),
  ).toBeVisible();

  await page.getByLabel('给创作 Agent 的指令').fill('写一个开场钩子');
  await page.getByRole('button', { name: '发送消息' }).click();
  await expect(
    page.getByRole('article', { name: '创作 Agent 的回复' }),
  ).toContainText('已生成开场钩子。');
  await waitForMockIdle(page);
  await expect(
    page.locator('.Toastify__toast-body').filter({ hasText: /canceled/i }),
  ).toHaveCount(0);

  const requests = getPageAudit(page).requests;
  expect(requests).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        method: 'POST',
        pathname: '/api/mujian/projects',
        body: {
          title: '雨夜便利店',
          synopsis: '一位失眠店员在雨夜遇见来自未来的客人。',
        },
      }),
      expect.objectContaining({
        method: 'POST',
        pathname: '/api/mujian/projects/e2e-project/agent/messages/stream',
        body: expect.objectContaining({
          content: '写一个开场钩子',
          session_id: 'e2e-session',
        }),
      }),
    ]),
  );
});

test('project modal closes with Escape and restores trigger focus', async ({
  page,
}) => {
  await visitRoute(page, routeById('projects'), {
    role: 'user',
    scenario: { projects: 'empty' },
  });
  const trigger = page.getByRole('button', { name: '新建项目' });

  await trigger.focus();
  await trigger.click();
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.getByLabel('项目名称')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('mobile console drawer supports Escape and restores menu focus', async ({
  page,
}) => {
  await page.setViewportSize(VIEWPORTS.mobile);
  await visitRoute(page, routeById('channel'), { role: 'admin' });
  const toggle = page.locator('#app-sidebar-toggle');

  await expect(toggle).toHaveAttribute('aria-expanded', 'false');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('#app-console-sidebar')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('#app-console-sidebar')).toHaveCount(0);
  await expect(toggle).toBeFocused();
});

test('public navigation exposes its current page', async ({ page }) => {
  await visitRoute(page, routeById('about'), { role: 'guest' });

  await expect(
    page.getByRole('navigation', { name: '主导航' }).getByRole('link', {
      name: '关于',
    }),
  ).toHaveAttribute('aria-current', 'page');
});

test('password reset form has labelled controls and announces validation errors', async ({
  page,
}) => {
  await visitRoute(page, routeById('reset-request'), { role: 'guest' });

  await expect(page.getByLabel('邮箱')).toBeVisible();
  await page.getByRole('button', { name: '提交' }).click();
  await expect(
    page.getByRole('alert').filter({ hasText: '请输入邮箱地址' }).first(),
  ).toBeVisible();
});

test('core authentication flow remains usable at 200% CSS zoom', async ({
  page,
}) => {
  await visitRoute(page, routeById('login'), { role: 'guest' });
  await page.evaluate(() => {
    document.documentElement.style.zoom = '2';
  });

  await expect(
    page.getByRole('button', { name: /登录/ }).first(),
  ).toBeVisible();
  await expect(page.getByRole('textbox').first()).toBeVisible();
  const viewport = await page.evaluate(() => ({
    width: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(viewport.scrollWidth).toBeLessThanOrEqual(viewport.width + 1);
});
