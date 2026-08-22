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

import { getUserIdFromLocalStorage } from '../../helpers';

const parseEventBlock = (block) => {
  let type = 'message';
  const dataLines = [];
  block.split('\n').forEach((line) => {
    if (line.startsWith('event:')) type = line.slice(6).trim();
    if (line.startsWith('data:')) dataLines.push(line.slice(5).trimStart());
  });
  if (dataLines.length === 0) return null;
  return { type, data: JSON.parse(dataLines.join('\n')) };
};

const responseError = async (response) => {
  try {
    const body = await response.json();
    return new Error(body.message || `Agent 调用失败（${response.status}）`);
  } catch {
    return new Error(`Agent 调用失败（${response.status}）`);
  }
};

export const streamAgentMessage = async ({
  projectId,
  payload,
  signal,
  onEvent,
}) => {
  const response = await fetch(
    `/api/mujian/projects/${projectId}/agent/messages/stream`,
    {
      method: 'POST',
      headers: {
        Accept: 'text/event-stream',
        'Content-Type': 'application/json',
        'New-Api-User': getUserIdFromLocalStorage(),
      },
      body: JSON.stringify(payload),
      signal,
    },
  );
  if (!response.ok) throw await responseError(response);
  if (!response.body) throw new Error('浏览器不支持流式响应');

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  const closeCompletedStream = async () => {
    try {
      await reader.cancel();
    } catch {
      // The completed event is authoritative even if the transport is already closed.
    }
  };
  try {
    while (true) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      buffer = buffer.replaceAll('\r\n', '\n');
      let boundary = buffer.indexOf('\n\n');
      while (boundary >= 0) {
        const event = parseEventBlock(buffer.slice(0, boundary));
        buffer = buffer.slice(boundary + 2);
        if (event?.type === 'error') {
          throw new Error(event.data.message || 'Agent 流式调用失败');
        }
        if (event) {
          onEvent(event);
          if (event.type === 'completed') {
            await closeCompletedStream();
            return;
          }
        }
        boundary = buffer.indexOf('\n\n');
      }
      if (done) break;
    }
    if (buffer.trim()) {
      const event = parseEventBlock(buffer.trim());
      if (event?.type === 'error') {
        throw new Error(event.data.message || 'Agent 流式调用失败');
      }
      if (event) {
        onEvent(event);
        if (event.type === 'completed') {
          await closeCompletedStream();
          return;
        }
      }
    }
    throw new Error('Agent 流式响应意外结束');
  } catch (error) {
    try {
      await reader.cancel();
    } catch {
      // The stream may already be closed by an AbortSignal.
    }
    throw error;
  } finally {
    reader.releaseLock();
  }
};
