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
import { buildNewTokenPayload, buildTokenBasicPayload } from './tokenPayload';

describe('token editor payloads', () => {
  test('edits only name and status', () => {
    expect(
      buildTokenBasicPayload({
        name: '  renamed token  ',
        enabled: false,
        key: 'must-not-be-submitted',
        expired_time: 2000000000,
        unlimited_quota: false,
        remain_quota: 321,
        model_limits_enabled: true,
        model_limits: 'gpt-4o',
        allow_ips: '192.0.2.10',
        group: 'auto',
        cross_group_retry: true,
      }),
    ).toEqual({
      name: 'renamed token',
      status: 2,
    });
  });

  test('creates a simple unrestricted token by default', () => {
    expect(buildNewTokenPayload({ name: 'new token', enabled: true })).toEqual({
      name: 'new token',
      status: 1,
      expired_time: -1,
      unlimited_quota: true,
      remain_quota: 0,
      model_limits_enabled: false,
      model_limits: '',
      allow_ips: '',
      group: '',
      cross_group_retry: false,
    });
  });
});
