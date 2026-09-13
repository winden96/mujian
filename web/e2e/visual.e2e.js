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

import {
  CORE_VISUAL_ROUTE_IDS,
  routeById,
  VIEWPORTS,
} from './support/route-manifest';
import { visitRoute, waitForMockIdle } from './support/mock-api';
import { expect, test } from './support/test-fixtures';

const settleVisuals = async (page) => {
  await page.addStyleTag({
    content: `
      *, *::before, *::after {
        animation: none !important;
        caret-color: transparent !important;
        transition: none !important;
      }
    `,
  });
  await waitForMockIdle(page);
  await page.evaluate(async () => {
    await document.fonts?.ready;
    await Promise.all(
      [...document.images].map(
        (image) =>
          new Promise((resolve) => {
            if (image.complete) {
              resolve();
              return;
            }
            image.addEventListener('load', resolve, { once: true });
            image.addEventListener('error', resolve, { once: true });
          }),
      ),
    );
    await new Promise((resolve) =>
      requestAnimationFrame(() => requestAnimationFrame(resolve)),
    );
  });
};

for (const [viewportName, viewport] of Object.entries(VIEWPORTS)) {
  test.describe(`${viewportName} visual baselines`, () => {
    test.use({ viewport });

    for (const routeId of CORE_VISUAL_ROUTE_IDS) {
      const route = routeById(routeId);
      test(`${routeId}`, async ({ page }) => {
        await visitRoute(page, route, { role: route.preferredRole });
        await settleVisuals(page);

        await expect(page).toHaveScreenshot(`${routeId}-${viewportName}.png`, {
          fullPage: false,
        });
      });
    }
  });
}
