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
import { buildMainNavLinks } from './useNavigation';

const translate = (text) => text;

describe('header navigation', () => {
  test('always builds the docs item as a same-origin application route', () => {
    const links = buildMainNavLinks(translate, {
      home: true,
      console: true,
      pricing: true,
      docs: true,
      about: true,
    });
    const docs = links.find((link) => link.itemKey === 'docs');

    expect(docs).toEqual({ text: '文档', itemKey: 'docs', to: '/docs' });
    expect(docs.isExternal).toBeUndefined();
    expect(docs.externalLink).toBeUndefined();
  });

  test('still hides docs when HeaderNavModules disables it', () => {
    const links = buildMainNavLinks(translate, {
      home: true,
      console: true,
      pricing: true,
      docs: false,
      about: true,
    });

    expect(links.some((link) => link.itemKey === 'docs')).toBe(false);
  });
});
