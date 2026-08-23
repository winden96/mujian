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

import { API } from '../../helpers';

const workspaceData = (response) => {
  if (!response.data?.success) {
    throw new Error(response.data?.message || '会话请求失败');
  }
  return response.data.data;
};

export const getAgentSessionWorkspace = async (projectId, sessionId, signal) =>
  workspaceData(
    await API.get(`/api/mujian/projects/${projectId}/workspace`, {
      signal,
      skipErrorHandler: true,
      params: sessionId ? { session_id: sessionId } : undefined,
    }),
  );

export const createAgentSession = async (projectId, signal) =>
  workspaceData(
    await API.post(
      `/api/mujian/projects/${projectId}/agent/sessions`,
      {},
      { signal, skipErrorHandler: true },
    ),
  );

export const clearAgentSession = async (projectId, sessionId, signal) =>
  workspaceData(
    await API.delete(
      `/api/mujian/projects/${projectId}/agent/sessions/${encodeURIComponent(sessionId)}/messages`,
      { signal, skipErrorHandler: true },
    ),
  );
