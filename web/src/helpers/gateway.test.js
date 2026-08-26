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
import { resolveApiBaseURL, resolveGatewayAddress } from './gateway';

describe('gateway address resolution', () => {
  test('prefers and normalizes a valid configured server address', () => {
    expect(
      resolveGatewayAddress(
        ' https://gateway.example.com/proxy/ ',
        'https://console.example.com',
      ),
    ).toBe('https://gateway.example.com/proxy');
  });

  test('falls back to the current same-origin address for invalid values', () => {
    expect(
      resolveGatewayAddress(
        'javascript:alert(1)',
        'https://mujian.example.com',
      ),
    ).toBe('https://mujian.example.com');
    expect(
      resolveGatewayAddress('//external.example.com', 'http://localhost:5173'),
    ).toBe('http://localhost:5173');
  });

  test('rejects credentials embedded in a configured address', () => {
    expect(
      resolveGatewayAddress(
        'https://user:secret@gateway.example.com',
        'https://mujian.example.com',
      ),
    ).toBe('https://mujian.example.com');
  });

  test('adds the OpenAI-compatible v1 prefix exactly once', () => {
    expect(resolveApiBaseURL('', 'https://mujian.example.com')).toBe(
      'https://mujian.example.com/v1',
    );
    expect(
      resolveApiBaseURL(
        'https://gateway.example.com/v1/',
        'https://mujian.example.com',
      ),
    ).toBe('https://gateway.example.com/v1');
    expect(
      resolveGatewayAddress(
        'https://gateway.example.com/v1/',
        'https://mujian.example.com',
      ),
    ).toBe('https://gateway.example.com');
    expect(resolveGatewayAddress('http://v1', 'https://fallback.example')).toBe(
      'http://v1',
    );
    expect(resolveApiBaseURL('http://v1', 'https://fallback.example')).toBe(
      'http://v1/v1',
    );
  });
});
