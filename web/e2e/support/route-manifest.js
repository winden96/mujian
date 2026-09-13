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

export const ROLE_FIXTURES = Object.freeze({
  guest: null,
  user: Object.freeze({
    id: 2,
    username: 'e2e-user',
    display_name: 'E2E 普通用户',
    role: 1,
    token: 'e2e-user-token',
    quota: 1000000,
    group: 'default',
    status: 1,
    setting: JSON.stringify({ language: 'zh' }),
  }),
  admin: Object.freeze({
    id: 10,
    username: 'e2e-admin',
    display_name: 'E2E 管理员',
    role: 10,
    token: 'e2e-admin-token',
    quota: 5000000,
    group: 'default',
    status: 1,
    setting: JSON.stringify({ language: 'zh' }),
  }),
  root: Object.freeze({
    id: 100,
    username: 'e2e-root',
    display_name: 'E2E Root',
    role: 100,
    token: 'e2e-root-token',
    quota: 10000000,
    group: 'default',
    status: 1,
    setting: JSON.stringify({ language: 'zh' }),
  }),
});

export const VIEWPORTS = Object.freeze({
  desktop: Object.freeze({ width: 1440, height: 900 }),
  tablet: Object.freeze({ width: 768, height: 1024 }),
  mobile: Object.freeze({ width: 390, height: 844 }),
});

const route = (id, pattern, path, access, preferredRole = 'guest') =>
  Object.freeze({ id, pattern, path, access, preferredRole });

// Keep this in App.jsx declaration order. Dynamic parameters are represented by
// stable, non-production fixture values so every entry can be navigated directly.
export const ROUTE_MANIFEST = Object.freeze([
  route('home', '/', '/', 'public'),
  route('setup', '/setup', '/setup', 'public'),
  route('forbidden', '/forbidden', '/forbidden', 'public'),
  route(
    'projects',
    '/console/mujian/projects',
    '/console/mujian/projects',
    'private',
    'user',
  ),
  route(
    'workspace',
    '/console/mujian/projects/:projectId/workspace',
    '/console/mujian/projects/e2e-project/workspace?session_id=e2e-session',
    'private',
    'user',
  ),
  route(
    'skills',
    '/console/mujian/skills',
    '/console/mujian/skills',
    'private',
    'user',
  ),
  route(
    'wallet',
    '/console/mujian/wallet',
    '/console/mujian/wallet',
    'private',
    'user',
  ),
  route(
    'integrations',
    '/console/mujian/integrations',
    '/console/mujian/integrations',
    'private',
    'user',
  ),
  route('models', '/console/models', '/console/models', 'admin', 'admin'),
  route(
    'deployment',
    '/console/deployment',
    '/console/deployment',
    'admin',
    'admin',
  ),
  route(
    'subscription',
    '/console/subscription',
    '/console/subscription',
    'admin',
    'admin',
  ),
  route('channel', '/console/channel', '/console/channel', 'admin', 'admin'),
  route('token', '/console/token', '/console/token', 'admin', 'admin'),
  route(
    'playground',
    '/console/playground',
    '/console/playground',
    'admin',
    'admin',
  ),
  route(
    'redemption',
    '/console/redemption',
    '/console/redemption',
    'admin',
    'admin',
  ),
  route('users', '/console/user', '/console/user', 'admin', 'admin'),
  route(
    'reset-confirm',
    '/user/reset',
    '/user/reset?email=e2e%40example.com&token=e2e-reset-token',
    'public',
  ),
  route('login', '/login', '/login', 'anonymous'),
  route('register', '/register', '/register', 'anonymous'),
  route('reset-request', '/reset', '/reset', 'public'),
  route(
    'oauth-github',
    '/oauth/github',
    '/oauth/github?code=e2e-code&state=e2e-state',
    'public',
  ),
  route(
    'oauth-discord',
    '/oauth/discord',
    '/oauth/discord?code=e2e-code&state=e2e-state',
    'public',
  ),
  route(
    'oauth-oidc',
    '/oauth/oidc',
    '/oauth/oidc?code=e2e-code&state=e2e-state',
    'public',
  ),
  route(
    'oauth-linuxdo',
    '/oauth/linuxdo',
    '/oauth/linuxdo?code=e2e-code&state=e2e-state',
    'public',
  ),
  route(
    'oauth-dynamic',
    '/oauth/:provider',
    '/oauth/custom?code=e2e-code&state=e2e-state',
    'public',
  ),
  route('setting', '/console/setting', '/console/setting', 'root', 'root'),
  route('personal', '/console/personal', '/console/personal', 'admin', 'admin'),
  route('topup', '/console/topup', '/console/topup', 'private', 'user'),
  route('log', '/console/log', '/console/log', 'private', 'user'),
  route('console', '/console', '/console', 'private', 'admin'),
  route(
    'midjourney',
    '/console/midjourney',
    '/console/midjourney',
    'private',
    'user',
  ),
  route('task', '/console/task', '/console/task', 'private', 'user'),
  route('pricing', '/pricing', '/pricing', 'public', 'user'),
  route('about', '/about', '/about', 'public'),
  route('docs', '/docs', '/docs', 'public'),
  route('user-agreement', '/user-agreement', '/user-agreement', 'public'),
  route('privacy-policy', '/privacy-policy', '/privacy-policy', 'public'),
  route('chat', '/console/chat/:id?', '/console/chat/e2e-chat', 'public'),
  route('chat2link', '/chat2link', '/chat2link', 'private', 'user'),
  route('not-found', '*', '/__e2e__/not-found', 'public'),
]);

export const EXPLICIT_ROUTE_COUNT = 39;

export const CORE_VISUAL_ROUTE_IDS = Object.freeze([
  'home',
  'about',
  'docs',
  'login',
  'pricing',
  'projects',
  'workspace',
  'wallet',
  'channel',
  'models',
  'setting',
  'forbidden',
  'not-found',
]);

export const routeById = (id) => {
  const match = ROUTE_MANIFEST.find((entry) => entry.id === id);
  if (!match) throw new Error(`Unknown E2E route id: ${id}`);
  return match;
};
