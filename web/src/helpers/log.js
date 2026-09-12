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

export function getLogOther(otherStr) {
  if (otherStr === undefined || otherStr === null || otherStr === '') {
    return {};
  }
  if (typeof otherStr === 'object') {
    return otherStr;
  }
  try {
    return JSON.parse(otherStr);
  } catch (e) {
    console.error(`Failed to parse record.other: "${otherStr}".`, e);
    return null;
  }
}

// Price renderers consume sales rates; persisted log metadata retains raw costs.
export function getSalesLogOther(otherStr) {
  const other = getLogOther(otherStr);
  if (!other?.sales_ratio) return other;
  const prices = { ...other };
  // Early managed sales logs used zero for the unused fixed-price field.
  if (prices.model_ratio > 0 && prices.model_price === 0) {
    prices.model_price = -1;
  }
  for (const field of [
    'model_ratio',
    'model_price',
    'web_search_price',
    'file_search_price',
    'image_generation_call_price',
    'audio_input_price',
  ]) {
    if (typeof prices[field] === 'number' && prices[field] >= 0) {
      prices[field] *= other.sales_ratio;
    }
  }
  return prices;
}
