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

import { ROUTE_MANIFEST, VIEWPORTS } from './support/route-manifest';
import { visitRoute, waitForMockIdle } from './support/mock-api';
import { expect, test } from './support/test-fixtures';

for (const viewportName of ['tablet', 'mobile']) {
  test.describe(`${viewportName} route smoke`, () => {
    test.use({ viewport: VIEWPORTS[viewportName] });

    for (const route of ROUTE_MANIFEST) {
      test(`${route.pattern} fits the viewport`, async ({ page }) => {
        const { pageErrors } = await visitRoute(page, route, {
          role: route.preferredRole,
        });
        await waitForMockIdle(page);

        const overflow = await page.evaluate(() => ({
          documentWidth: document.documentElement.scrollWidth,
          viewportWidth: document.documentElement.clientWidth,
        }));
        expect(overflow.documentWidth).toBeLessThanOrEqual(
          overflow.viewportWidth + 1,
        );
        expect(pageErrors, pageErrors.map(String).join('\n')).toEqual([]);
      });
    }
  });
}
