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

import { expect, test } from '@playwright/test';

const apiToken = process.env.MUJIAN_REAL_API_TOKEN;
const chatModel = process.env.MUJIAN_REAL_CHAT_MODEL || 'deepseek-v4-flash';

test.skip(!apiToken, 'requires an explicitly provided real API token');
test.describe.configure({ retries: 0, mode: 'serial' });
test.use({ trace: 'off', screenshot: 'off', video: 'off' });

const openOperation = async (page, methodClass, summary) => {
  const operation = page
    .locator(`.swagger-ui .opblock.${methodClass}`)
    .filter({ hasText: summary })
    .first();
  await operation.locator('.opblock-summary').click();
  return operation;
};

const liveResponse = (operation) =>
  operation.locator('.live-responses-table').first();

const responseStatus = (operation) =>
  liveResponse(operation).locator('tbody .response-col_status').first();

const responseBody = (operation) =>
  liveResponse(operation)
    .locator('tbody .response-col_description pre.microlight')
    .first();

test('docs UI reaches the real gateway and isolates a controlled failure', async ({
  page,
}) => {
  test.setTimeout(120000);
  await page.goto('/docs', { waitUntil: 'domcontentloaded' });
  await expect(page.locator('.swagger-ui')).toBeVisible();

  await page
    .getByRole('button', { name: /Authorize/ })
    .first()
    .click();
  const authModal = page.locator('.swagger-ui .dialog-ux');
  await authModal.locator('input[type="text"]').evaluate((input, token) => {
    const setValue = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      'value',
    ).set;
    setValue.call(input, token);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.dispatchEvent(new Event('change', { bubbles: true }));
  }, apiToken);
  await authModal.locator('button.authorize').click();
  await authModal.locator('button.close-modal').click();

  const modelsOperation = await openOperation(
    page,
    'opblock-get',
    '列出可用模型',
  );
  await modelsOperation.locator('.try-out__btn').click();
  await modelsOperation.locator('.execute').click();
  await expect(responseStatus(modelsOperation)).toHaveText('200');
  await expect(responseBody(modelsOperation)).toContainText('data');

  const chatOperation = await openOperation(
    page,
    'opblock-post',
    '创建对话补全',
  );
  await chatOperation.locator('.try-out__btn').click();
  const requestBody = chatOperation.locator('textarea');
  await requestBody.fill(
    JSON.stringify({
      model: chatModel,
      messages: [{ role: 'user', content: 'Reply with OK.' }],
      max_tokens: 1,
      stream: false,
    }),
  );
  await chatOperation.locator('.execute').click();
  await expect(responseStatus(chatOperation)).toHaveText('200', {
    timeout: 90000,
  });
  await expect(responseBody(chatOperation)).toContainText('choices');

  await requestBody.fill(
    JSON.stringify({
      model: 'mujian-controlled-invalid-model',
      messages: [{ role: 'user', content: 'This request must fail.' }],
      max_tokens: 1,
      stream: false,
    }),
  );
  await chatOperation.locator('.execute').click();
  await expect(responseStatus(chatOperation)).toHaveText(/^(400|403|404)$/);
});
