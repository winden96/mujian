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

import { existsSync } from 'node:fs';
import { chromium, defineConfig } from '@playwright/test';

const requestedBaseURL = new URL(
  process.env.PLAYWRIGHT_BASE_URL || 'http://127.0.0.1:4179',
);
const loopbackHosts = new Set(['127.0.0.1', 'localhost', '[::1]']);
if (
  !['http:', 'https:'].includes(requestedBaseURL.protocol) ||
  !loopbackHosts.has(requestedBaseURL.hostname)
) {
  throw new Error(
    `Playwright only accepts a loopback base URL, received: ${requestedBaseURL.origin}`,
  );
}
const baseURL = requestedBaseURL.origin;
const skipWebServer = process.env.PLAYWRIGHT_SKIP_WEBSERVER === '1';
if (!skipWebServer && baseURL !== 'http://127.0.0.1:4179') {
  throw new Error(
    'Set PLAYWRIGHT_SKIP_WEBSERVER=1 when using a custom loopback base URL.',
  );
}
const configuredChannel = process.env.PLAYWRIGHT_CHANNEL;
const browserChannel =
  configuredChannel ||
  (existsSync(chromium.executablePath()) ? undefined : 'chrome');

export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.e2e.js',
  outputDir: './output/playwright/results',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : 4,
  timeout: 60000,
  expect: {
    timeout: 7500,
    toHaveScreenshot: {
      animations: 'disabled',
      maxDiffPixelRatio: 0.01,
    },
  },
  reporter: [
    ['line'],
    [
      'html',
      {
        outputFolder: './output/playwright/report',
        open: 'never',
      },
    ],
  ],
  webServer: skipWebServer
    ? undefined
    : {
        command: 'bun run dev -- --host 127.0.0.1 --port 4179',
        url: baseURL,
        reuseExistingServer: false,
        timeout: 120000,
      },
  use: {
    baseURL,
    browserName: 'chromium',
    channel: browserChannel,
    colorScheme: 'light',
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    reducedMotion: 'reduce',
    serviceWorkers: 'block',
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
});
