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

const normalizeHttpAddress = (value) => {
  if (typeof value !== 'string' || value.trim() === '') return '';

  try {
    const address = new URL(value.trim());
    if (!['http:', 'https:'].includes(address.protocol)) return '';
    if (address.username || address.password) return '';

    address.search = '';
    address.hash = '';
    address.pathname = address.pathname.replace(/\/+$/, '');
    return address.toString().replace(/\/$/, '');
  } catch {
    return '';
  }
};

export const resolveGatewayAddress = (serverAddress, currentOrigin) => {
  const address =
    normalizeHttpAddress(serverAddress) || normalizeHttpAddress(currentOrigin);
  if (!address) return '';

  const gateway = new URL(address);
  if (gateway.pathname.endsWith('/v1')) {
    gateway.pathname = gateway.pathname.slice(0, -3) || '/';
  }
  return gateway.toString().replace(/\/$/, '');
};

export const resolveApiBaseURL = (serverAddress, currentOrigin) => {
  const gatewayAddress = resolveGatewayAddress(serverAddress, currentOrigin);
  return gatewayAddress ? `${gatewayAddress}/v1` : '';
};
