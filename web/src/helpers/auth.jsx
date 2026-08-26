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

import React from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import {
  getPostLoginPath,
  normalizeAuthReturnTarget,
  withAuthReturnTarget,
} from './authReturn';

export function authHeader() {
  // return authorization header with jwt token
  let user = JSON.parse(localStorage.getItem('user'));

  if (user && user.token) {
    return { Authorization: 'Bearer ' + user.token };
  } else {
    return {};
  }
}

export const AuthRedirect = ({ children }) => {
  const location = useLocation();
  const rawUser = localStorage.getItem('user');

  if (rawUser) {
    try {
      const user = JSON.parse(rawUser);
      const requestedReturnTo = normalizeAuthReturnTarget(
        new URLSearchParams(location.search).get('next'),
      );
      return (
        <Navigate to={getPostLoginPath(user, requestedReturnTo)} replace />
      );
    } catch {
      localStorage.removeItem('user');
    }
  }

  return children;
};

function PrivateRoute({ children }) {
  const location = useLocation();
  if (!localStorage.getItem('user')) {
    const returnTo = normalizeAuthReturnTarget(location.pathname);
    return (
      <Navigate
        to={withAuthReturnTarget('/login', returnTo)}
        state={{ from: location }}
        replace
      />
    );
  }
  return children;
}

function RoleRoute({ children, minimumRole }) {
  const location = useLocation();
  const raw = localStorage.getItem('user');
  if (!raw) {
    const returnTo = normalizeAuthReturnTarget(location.pathname);
    return (
      <Navigate
        to={withAuthReturnTarget('/login', returnTo)}
        state={{ from: location }}
        replace
      />
    );
  }
  try {
    const user = JSON.parse(raw);
    if (user && typeof user.role === 'number' && user.role >= minimumRole) {
      return children;
    }
  } catch {
    // ignore
  }
  return <Navigate to='/forbidden' replace />;
}

export function AdminRoute({ children }) {
  return <RoleRoute minimumRole={10}>{children}</RoleRoute>;
}

export function RootRoute({ children }) {
  return <RoleRoute minimumRole={100}>{children}</RoleRoute>;
}

export { PrivateRoute };
