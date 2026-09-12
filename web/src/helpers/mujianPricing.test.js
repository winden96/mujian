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
