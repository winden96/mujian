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

import { readFile } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import { EXPLICIT_ROUTE_COUNT, ROUTE_MANIFEST } from './support/route-manifest';

test('route manifest mirrors every App route', async () => {
  const appSource = await readFile(
    new URL('../src/App.jsx', import.meta.url),
    'utf8',
  );
  const declaredPaths = [...appSource.matchAll(/\bpath='([^']+)'/g)].map(
    ([, path]) => path,
  );
  const manifestPaths = ROUTE_MANIFEST.map(({ pattern }) => pattern);

  expect(declaredPaths.filter((path) => path !== '*')).toHaveLength(
    EXPLICIT_ROUTE_COUNT,
  );
  expect(declaredPaths).toEqual(manifestPaths);
  expect(new Set(manifestPaths).size).toBe(ROUTE_MANIFEST.length);
});
