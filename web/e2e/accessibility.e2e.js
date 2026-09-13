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

import AxeBuilder from '@axe-core/playwright';
import { ROUTE_MANIFEST } from './support/route-manifest';
import { test, expect } from './support/test-fixtures';
import { visitRoute, waitForMockIdle } from './support/mock-api';

for (const route of ROUTE_MANIFEST) {
  test(`${route.pattern} has no serious or critical axe violations`, async ({
    page,
  }, testInfo) => {
    await visitRoute(page, route, { role: route.preferredRole });
    await waitForMockIdle(page);

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    const blockingViolations = results.violations.filter(({ impact }) =>
      ['serious', 'critical'].includes(impact),
    );
    const evidence = blockingViolations.map(
      ({ id, impact, help, helpUrl, nodes }) => ({
        id,
        impact,
        help,
        helpUrl,
        nodes: nodes.map(({ target, html, failureSummary }) => ({
          target,
          html,
          failureSummary,
        })),
      }),
    );

    if (evidence.length) {
      await testInfo.attach('axe-serious-critical.json', {
        body: JSON.stringify(evidence, null, 2),
        contentType: 'application/json',
      });
      if (process.env.PLAYWRIGHT_AXE_DEBUG === '1') {
        console.log(JSON.stringify(evidence, null, 2));
      }
    }

    expect(
      evidence.map(
        ({ id, impact, help, nodes }) =>
          `[${impact}] ${id}: ${help} (${nodes.length} nodes)`,
      ),
      'See the axe-serious-critical.json attachment for exact DOM targets.',
    ).toEqual([]);
  });
}
