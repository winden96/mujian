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

import { expect, test } from 'bun:test';
import { chromium } from '@playwright/test';
import { readFileSync } from 'node:fs';

// Real HTTP servers are required: Playwright route mocks disable browser caching.
test('downloads original bytes after a non-CORS image preview has been cached', async () => {
  const png = Buffer.from(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jvZkAAAAASUVORK5CYII=',
    'base64',
  );
  const requests = [];
  const images = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch(request) {
      const origin = request.headers.get('origin');
      requests.push(origin);
      return new Response(png, {
        headers: {
          'Content-Type': 'image/png',
          'Cache-Control': 'public, max-age=31536000, immutable',
          ...(origin ? { 'Access-Control-Allow-Origin': origin } : {}),
        },
      });
    },
  });
  const app = Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    fetch(request) {
      if (new URL(request.url).pathname === '/imageContent.js') {
        return new Response(
          readFileSync(new URL('./imageContent.js', import.meta.url)),
          { headers: { 'Content-Type': 'text/javascript' } },
        );
      }
      return new Response(
        `<img src="${images.url}result.png"><button>Download</button>
        <script type="module">
          import { getExternalImageContentBlob, saveImageBlob } from '/imageContent.js';
          document.querySelector('button').onclick = async () => {
            const blob = await getExternalImageContentBlob(document.querySelector('img').src);
            saveImageBlob(blob, 'cached-preview');
          };
        </script>`,
        { headers: { 'Content-Type': 'text/html' } },
      );
    },
  });
  let browser;
  try {
    browser = await chromium.launch({ channel: 'chrome', headless: true });
    const page = await browser.newPage();
    await page.goto(app.url.href);
    expect(await page.locator('img').evaluate((img) => img.naturalWidth)).toBe(
      1,
    );
    expect(requests[0]).toBeNull();
    const cachedFetch = await page.evaluate(async () => {
      try {
        await fetch(document.querySelector('img').src, { credentials: 'omit' });
        return 'unexpected success';
      } catch (error) {
        return error.name;
      }
    });
    expect(cachedFetch).toBe('TypeError');
    for (let attempt = 0; attempt < 2; attempt++) {
      const downloaded = page.waitForEvent('download');
      await page.getByRole('button', { name: 'Download' }).click();
      const download = await downloaded;
      expect(download.suggestedFilename()).toBe(
        'mujian-image-cached-preview.png',
      );
      expect(await download.failure()).toBeNull();
      expect(readFileSync(await download.path()).equals(png)).toBe(true);
    }
    expect(requests.slice(1)).toEqual([app.url.origin, app.url.origin]);
  } finally {
    await browser?.close();
    app.stop(true);
    images.stop(true);
  }
}, 30000);
