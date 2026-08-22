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

import { beforeAll, describe, expect, test } from 'bun:test';

if (!globalThis.localStorage) {
  globalThis.localStorage = {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
  };
}

let getExternalImageContentBlob;
let imageContentExtension;
let isExternalImageContentUrl;
let saveImageBlob;

beforeAll(async () => {
  const helpers = await import('./imageContent');
  getExternalImageContentBlob = helpers.getExternalImageContentBlob;
  imageContentExtension = helpers.imageContentExtension;
  isExternalImageContentUrl = helpers.isExternalImageContentUrl;
  saveImageBlob = helpers.saveImageBlob;
});

describe('Mujian image content URLs', () => {
  test('classifies public OBS URLs without treating authenticated API paths as external', () => {
    expect(
      isExternalImageContentUrl(
        'https://static.mujianai.com/mujian/prod/public/result.png',
      ),
    ).toBe(true);
    expect(
      isExternalImageContentUrl('/api/mujian/projects/project/image'),
    ).toBe(false);
  });

  test('derives a safe download extension from MIME type or URL path', () => {
    expect(imageContentExtension('', 'image/jpeg')).toBe('jpg');
    expect(
      imageContentExtension(
        'https://static.mujianai.com/result.webp?version=1',
      ),
    ).toBe('webp');
    expect(imageContentExtension('https://example.com/result.svg')).toBe('');
  });

  test('downloads public images without credentials, session ids, or API headers', async () => {
    const originalFetch = globalThis.fetch;
    let capturedUrl = '';
    let capturedOptions;
    globalThis.fetch = async (url, options) => {
      capturedUrl = url;
      capturedOptions = options;
      return new Response(new Blob(['png'], { type: 'image/png' }), {
        status: 200,
      });
    };
    try {
      const blob = await getExternalImageContentBlob(
        'https://static.mujianai.com/mujian/prod/public/result.png',
      );
      expect(blob.type).toBe('image/png');
      expect(capturedUrl).not.toContain('secret-session-id');
      expect(capturedOptions.credentials).toBe('omit');
      expect(capturedOptions.referrerPolicy).toBe('no-referrer');
      expect(capturedOptions.headers).toBeUndefined();
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  test('rejects a non-image response from the public object domain', async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = async () =>
      new Response('<html>not an image</html>', {
        status: 200,
        headers: { 'Content-Type': 'text/html' },
      });
    try {
      await expect(
        getExternalImageContentBlob('https://static.mujianai.com/result.png'),
      ).rejects.toThrow('不支持的文件格式');
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  test('keeps the object URL alive until the browser has received the download click', () => {
    const originalDocument = globalThis.document;
    const originalCreateObjectURL = URL.createObjectURL;
    const originalRevokeObjectURL = URL.revokeObjectURL;
    const originalSetTimeout = globalThis.setTimeout;
    const actions = [];
    const link = {
      click: () => actions.push('click'),
      remove: () => actions.push('remove'),
    };
    globalThis.document = {
      createElement: () => link,
      body: { appendChild: () => actions.push('append') },
    };
    URL.createObjectURL = () => 'blob:test-image';
    URL.revokeObjectURL = () => actions.push('revoke');
    globalThis.setTimeout = (callback) => {
      actions.push('schedule-revoke');
      callback();
      return 1;
    };
    try {
      saveImageBlob(new Blob(['png'], { type: 'image/png' }), 'generation-a');
      expect(link.href).toBe('blob:test-image');
      expect(link.download).toBe('mujian-image-generation-a.png');
      expect(actions).toEqual([
        'append',
        'click',
        'remove',
        'schedule-revoke',
        'revoke',
      ]);
    } finally {
      globalThis.document = originalDocument;
      URL.createObjectURL = originalCreateObjectURL;
      URL.revokeObjectURL = originalRevokeObjectURL;
      globalThis.setTimeout = originalSetTimeout;
    }
  });
});
