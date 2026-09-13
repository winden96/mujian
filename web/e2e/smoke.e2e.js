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

import { ROLE_FIXTURES, ROUTE_MANIFEST } from './support/route-manifest';
import { visitRoute, waitForMockIdle } from './support/mock-api';
import { expect, test } from './support/test-fixtures';

const expectedPath = (route, role) => {
  if (route.id.startsWith('oauth-')) {
    return role === 'admin' || role === 'root'
      ? '/console'
      : '/console/mujian/projects';
  }
  if (route.access === 'private' && role === 'guest') return '/login';
  if (route.access === 'admin') {
    if (role === 'guest') return '/login';
    if (role === 'user') return '/forbidden';
  }
  if (route.access === 'root') {
    if (role === 'guest') return '/login';
    if (role !== 'root') return '/forbidden';
  }
  if (route.access === 'anonymous' && role !== 'guest') {
    return role === 'user' ? '/console/mujian/projects' : '/console';
  }
  if (route.id === 'console' && role === 'user') {
    return '/console/mujian/projects';
  }
  return new URL(route.path, 'http://e2e.local').pathname;
};

for (const role of Object.keys(ROLE_FIXTURES)) {
  test.describe(`${role} route smoke`, () => {
    for (const route of ROUTE_MANIFEST) {
      test(`${route.pattern}`, async ({ page }) => {
        const { pageErrors } = await visitRoute(page, route, { role });
        const targetPath = expectedPath(route, role);

        await expect
          .poll(() => new URL(page.url()).pathname, {
            message: `${role} access result for ${route.pattern}`,
          })
          .toBe(targetPath);
        await expect(page.locator('#root')).not.toBeEmpty();
        await expect(page.locator('html')).not.toHaveClass(/\bdark\b/);
        await waitForMockIdle(page);
        expect(pageErrors, pageErrors.map(String).join('\n')).toEqual([]);
      });
    }
  });
}
