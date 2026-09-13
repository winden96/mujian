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
import { mujianPriceText, mujianModelOption } from './mujianPricing';
import { getSalesLogOther } from './log';

describe('sales pricing display', () => {
  test('keeps fractional-cent catalog prices and shows them in model options', () => {
    const item = {
      id: 'deepseek',
      name: 'DeepSeek',
      available: true,
      billing_type: 'token',
      min_input_price: 0.1875,
      max_input_price: 0.1875,
      min_output_price: 0.375,
      max_output_price: 0.375,
    };
    expect(mujianPriceText(item)).toBe('输入 $0.1875 · 输出 $0.375 / 1M');
    expect(mujianModelOption('deepseek', [item]).label).toContain('$0.1875');
  });
  test('keeps raw costs intact and does not alter historical logs', () => {
    const raw = {
      sales_ratio: 1.25,
      model_ratio: 0.8,
      model_price: -1,
      cache_ratio: 0.1,
      web_search_price: 10,
    };
    const sold = getSalesLogOther(raw);
    expect(sold.model_ratio).toBe(1);
    expect(sold.model_price).toBe(-1);
    expect(sold.cache_ratio).toBe(0.1);
    expect(sold.web_search_price).toBe(12.5);
    expect(raw.model_ratio).toBe(0.8);
    expect(getSalesLogOther(JSON.stringify(raw))).toEqual(sold);
    expect(getSalesLogOther({ model_ratio: 0.8 }).model_ratio).toBe(0.8);
  });
  test('renders early sales token logs as token prices and preserves fixed rates', () => {
    expect(
      getSalesLogOther({ sales_ratio: 1.25, model_ratio: 2.5, model_price: 0 })
        .model_price,
    ).toBe(-1);
    expect(
      getSalesLogOther({ sales_ratio: 1.25, model_ratio: 0, model_price: 0.8 })
        .model_price,
    ).toBe(1);
  });
  test('shows fixed and unavailable prices', () => {
    expect(
      mujianPriceText({
        available: true,
        billing_type: 'fixed',
        min_fixed_price: 1,
        max_fixed_price: 2,
      }),
    ).toBe('$1–$2 / 次');
    expect(mujianPriceText({ available: false })).toBe('暂无报价');
  });
});

test('shows image output pricing separately from text output', () => {
  expect(
    mujianPriceText({
      available: true,
      billing_type: 'token',
      min_input_price: 10,
      max_input_price: 10,
      min_output_price: 10,
      max_output_price: 10,
      min_image_output_price: 37.5,
      max_image_output_price: 37.5,
    }),
  ).toBe('输入 $10 · 文字输出 $10 · 图片输出 $37.5 / 1M');
});

test('GPT image retail is CNY per image regardless of upstream token prices', () => {
  const item = {
    available: true,
    billing_type: 'token',
    max_image_output_price: 37.5,
    retail_pricing: { currency: 'CNY', unit: 'image', amount: 0.2 },
  };
  expect(mujianPriceText(item)).toBe('¥0.20/张');
  expect(mujianPriceText({ ...item, available: false })).toBe('暂无报价');
});

test('Nano option displays the final CNY price per request', () => {
  const item = {
    id: 'nano-banana-2',
    name: 'Nano Banana 2',
    available: true,
    billing_type: 'fixed',
    min_fixed_price: 0.0275,
    max_fixed_price: 0.0275,
    retail_pricing: { currency: 'CNY', unit: 'request', amount: 0.2 },
  };
  expect(mujianModelOption(item.id, [item]).label).toBe(
    'Nano Banana 2 · ¥0.20/次',
  );
});
