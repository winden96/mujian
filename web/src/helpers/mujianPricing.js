const money = (value) =>
  `$${Number(value).toLocaleString('en-US', { maximumFractionDigits: 6 })}`;
const range = (min, max) =>
  min === max ? money(min) : `${money(min)}–${money(max)}`;

export function mujianPriceText(item) {
  if (!item?.available) return '暂无报价';
  if (item.billing_type === 'fixed')
    return `${range(item.min_fixed_price, item.max_fixed_price)} / 次`;
  return `输入 ${range(item.min_input_price, item.max_input_price)} · 输出 ${range(item.min_output_price, item.max_output_price)} / 1M`;
}

export function mujianModelOption(id, items) {
  const item = items.find((entry) => entry.id === id);
  return { value: id, label: `${item?.name || id} · ${mujianPriceText(item)}` };
}
