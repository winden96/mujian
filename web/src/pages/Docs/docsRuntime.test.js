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

import { describe, expect, test } from 'bun:test';
import {
  createQuickstarts,
  omitRequestCredentials,
  withRuntimeServer,
} from './docsRuntime';

describe('docs runtime configuration', () => {
  test('uses the resolved gateway for interactive OpenAPI requests', () => {
    const original = { openapi: '3.0.3', servers: [{ url: 'https://old' }] };
    const runtime = withRuntimeServer(original, 'https://api.example.com');

    expect(runtime.servers).toEqual([
      { url: 'https://api.example.com', description: '当前幕间网关' },
    ]);
    expect(original.servers).toEqual([{ url: 'https://old' }]);
  });

  test('forces browser requests to omit console session cookies', () => {
    const request = { url: 'https://api.example.com/v1/models' };

    expect(omitRequestCredentials(request)).toBe(request);
    expect(request.credentials).toBe('omit');
  });

  test('builds all quickstarts against the same API base URL', () => {
    const snippets = createQuickstarts('https://api.example.com/v1');

    expect(snippets.map((snippet) => snippet.id)).toEqual([
      'curl',
      'python',
      'node',
    ]);
    for (const snippet of snippets) {
      expect(snippet.code).toContain('https://api.example.com/v1');
    }
  });
});
