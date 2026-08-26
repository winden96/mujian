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
  MUJIAN_INTEGRATIONS_PATH,
  MUJIAN_TOKEN_PATH,
  consumeOAuthReturnTarget,
  getPostLoginPath,
  normalizeAuthReturnTarget,
  storeOAuthReturnTarget,
  withAuthReturnTarget,
} from './authReturn';

function createStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
  };
}

describe('authentication return targets', () => {
  test('allows only exact Mujian return paths', () => {
    expect(normalizeAuthReturnTarget(MUJIAN_INTEGRATIONS_PATH)).toBe(
      MUJIAN_INTEGRATIONS_PATH,
    );
    expect(normalizeAuthReturnTarget(MUJIAN_TOKEN_PATH)).toBe(
      MUJIAN_TOKEN_PATH,
    );

    for (const target of [
      'https://evil.example/console/mujian/integrations',
      '//evil.example/console/mujian/integrations',
      '/console',
      '/console/token/',
      '/console/token?tab=new',
      '/console/mujian/integrations/',
      '/console/mujian/integrations?tab=token',
      '/console/mujian/integrations#token',
      '',
      null,
    ]) {
      expect(normalizeAuthReturnTarget(target)).toBeNull();
    }
  });

  test('falls back to the existing role home for missing or invalid targets', () => {
    expect(getPostLoginPath({ role: 1 }, null)).toBe(
      '/console/mujian/projects',
    );
    expect(getPostLoginPath({ role: 10 }, 'https://evil.example')).toBe(
      '/console',
    );
    expect(getPostLoginPath({ role: 1 }, MUJIAN_INTEGRATIONS_PATH)).toBe(
      MUJIAN_INTEGRATIONS_PATH,
    );
    expect(getPostLoginPath({ role: 1 }, MUJIAN_TOKEN_PATH)).toBe(
      MUJIAN_TOKEN_PATH,
    );
    expect(withAuthReturnTarget('/login', MUJIAN_INTEGRATIONS_PATH)).toBe(
      '/login?next=%2Fconsole%2Fmujian%2Fintegrations',
    );
    expect(withAuthReturnTarget('/login', MUJIAN_TOKEN_PATH)).toBe(
      '/login?next=%2Fconsole%2Ftoken',
    );
    expect(withAuthReturnTarget('/login', '//evil.example')).toBe('/login');
  });

  test('binds an OAuth target to state and consumes it exactly once', () => {
    const storage = createStorage();

    expect(
      storeOAuthReturnTarget(
        'expected-state',
        MUJIAN_INTEGRATIONS_PATH,
        storage,
      ),
    ).toBe(true);
    expect(consumeOAuthReturnTarget('other-state', storage)).toBeNull();
    expect(consumeOAuthReturnTarget('expected-state', storage)).toBe(
      MUJIAN_INTEGRATIONS_PATH,
    );
    expect(consumeOAuthReturnTarget('expected-state', storage)).toBeNull();
  });

  test('never stores an invalid OAuth target', () => {
    const storage = createStorage();

    expect(
      storeOAuthReturnTarget('state', 'https://evil.example', storage),
    ).toBe(false);
    expect(consumeOAuthReturnTarget('state', storage)).toBeNull();
  });
});
