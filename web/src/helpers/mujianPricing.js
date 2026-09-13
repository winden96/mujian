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

const money = (value) =>
  `$${Number(value).toLocaleString('en-US', { maximumFractionDigits: 6 })}`;
const range = (min, max) =>
  min === max ? money(min) : `${money(min)}–${money(max)}`;

export function mujianPriceText(item) {
  if (!item?.available) return '暂无报价';
  if (
    item.retail_pricing?.currency === 'CNY' &&
    ['image', 'request'].includes(item.retail_pricing?.unit)
  )
    return `¥${Number(item.retail_pricing.amount).toFixed(2)}/${item.retail_pricing.unit === 'request' ? '次' : '张'}`;
  if (item.billing_type === 'fixed')
    return `${range(item.min_fixed_price, item.max_fixed_price)} / 次`;
  if (item.max_image_output_price > 0)
    return `输入 ${range(item.min_input_price, item.max_input_price)} · 文字输出 ${range(item.min_output_price, item.max_output_price)} · 图片输出 ${range(item.min_image_output_price, item.max_image_output_price)} / 1M`;
  return `输入 ${range(item.min_input_price, item.max_input_price)} · 输出 ${range(item.min_output_price, item.max_output_price)} / 1M`;
}

export function mujianModelOption(id, items) {
  const item = items.find((entry) => entry.id === id);
  return { value: id, label: `${item?.name || id} · ${mujianPriceText(item)}` };
}
