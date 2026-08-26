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

export const MUJIAN_INTEGRATIONS_PATH = '/console/mujian/integrations';
export const MUJIAN_TOKEN_PATH = '/console/token';

const ALLOWED_AUTH_RETURN_TARGETS = new Set([
  MUJIAN_INTEGRATIONS_PATH,
  MUJIAN_TOKEN_PATH,
]);

const OAUTH_RETURN_STORAGE_PREFIX = 'mujian:oauth:return:';

export function normalizeAuthReturnTarget(value) {
  return ALLOWED_AUTH_RETURN_TARGETS.has(value) ? value : null;
}

export function getPostLoginPath(user, requestedTarget) {
  return (
    normalizeAuthReturnTarget(requestedTarget) ||
    (user?.role >= 10 ? '/console' : '/console/mujian/projects')
  );
}

export function withAuthReturnTarget(path, requestedTarget) {
  const target = normalizeAuthReturnTarget(requestedTarget);
  if (!target) {
    return path;
  }

  const separator = path.includes('?') ? '&' : '?';
  return `${path}${separator}next=${encodeURIComponent(target)}`;
}

function oauthReturnStorageKey(state) {
  return `${OAUTH_RETURN_STORAGE_PREFIX}${state}`;
}

function browserSessionStorage() {
  try {
    return globalThis.sessionStorage;
  } catch {
    return undefined;
  }
}

export function storeOAuthReturnTarget(
  state,
  requestedTarget,
  storage = browserSessionStorage(),
) {
  const target = normalizeAuthReturnTarget(requestedTarget);
  if (typeof state !== 'string' || !state || !target || !storage) {
    return false;
  }

  try {
    storage.setItem(
      oauthReturnStorageKey(state),
      JSON.stringify({ state, target }),
    );
    return true;
  } catch {
    return false;
  }
}

export function consumeOAuthReturnTarget(
  state,
  storage = browserSessionStorage(),
) {
  if (typeof state !== 'string' || !state || !storage) {
    return null;
  }

  const key = oauthReturnStorageKey(state);
  let serialized;
  try {
    serialized = storage.getItem(key);
    if (serialized) {
      storage.removeItem(key);
    }
  } catch {
    return null;
  }
  if (!serialized) {
    return null;
  }

  let record;
  try {
    record = JSON.parse(serialized);
  } catch {
    return null;
  }

  if (
    record?.state !== state ||
    normalizeAuthReturnTarget(record?.target) === null
  ) {
    return null;
  }
  return record.target;
}
