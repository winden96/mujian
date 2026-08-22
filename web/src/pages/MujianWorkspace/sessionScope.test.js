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

import { describe, expect, test } from 'bun:test';
import {
  generationMatchesScope,
  hasWorkspaceDraft,
  mergeAuthoritativeSessionMetadata,
} from './sessionScope';

describe('Mujian workspace session scope', () => {
  test('accepts an image generation only for its originating project and session', () => {
    const generation = { project_id: 'project-a', session_id: 'session-a' };

    expect(generationMatchesScope(generation, 'project-a', 'session-a')).toBe(
      true,
    );
    expect(generationMatchesScope(generation, 'project-b', 'session-a')).toBe(
      false,
    );
    expect(generationMatchesScope(generation, 'project-a', 'session-b')).toBe(
      false,
    );
  });

  test('treats chat and image inputs as one discardable workspace draft', () => {
    const emptyDraft = {
      message: ' ',
      attachments: [],
      imagePrompt: '',
      imageReferences: [],
    };

    expect(hasWorkspaceDraft(emptyDraft)).toBe(false);
    expect(hasWorkspaceDraft({ ...emptyDraft, message: '写一个开场' })).toBe(
      true,
    );
    expect(
      hasWorkspaceDraft({ ...emptyDraft, attachments: [{ id: 'file' }] }),
    ).toBe(true);
    expect(hasWorkspaceDraft({ ...emptyDraft, imagePrompt: '雨夜街头' })).toBe(
      true,
    );
    expect(
      hasWorkspaceDraft({ ...emptyDraft, imageReferences: [{ id: 'image' }] }),
    ).toBe(true);
  });

  test('does not merge authoritative metadata from another project or session', () => {
    const current = {
      project: { id: 'project-a' },
      active_session_id: 'session-a',
      agent_sessions: [{ id: 'session-a', title: '当前标题', updated_at: 100 }],
      messages: [{ id: 'message-a' }],
    };

    const wrongProject = {
      project: { id: 'project-b' },
      active_session_id: 'session-a',
      agent_sessions: [{ id: 'session-a', title: '错误项目', updated_at: 200 }],
    };
    const wrongSession = {
      project: { id: 'project-a' },
      active_session_id: 'session-b',
      agent_sessions: [{ id: 'session-b', title: '错误会话', updated_at: 200 }],
    };

    expect(
      mergeAuthoritativeSessionMetadata(
        current,
        wrongProject,
        'project-a',
        'session-a',
      ),
    ).toBe(current);
    expect(
      mergeAuthoritativeSessionMetadata(
        current,
        wrongSession,
        'project-a',
        'session-a',
      ),
    ).toBe(current);
  });

  test('uses server session titles and sorting without replacing workspace content', () => {
    const messages = [{ id: 'message-a', content: '即时消息' }];
    const imageGenerations = [{ id: 'generation-a', status: 'queued' }];
    const current = {
      project: { id: 'project-a' },
      active_session_id: 'session-a',
      agent_sessions: [
        { id: 'session-a', title: '新会话', updated_at: 100 },
        { id: 'session-b', title: '旧会话', updated_at: 90 },
      ],
      messages,
      image_generations: imageGenerations,
    };
    const authoritative = {
      project: { id: 'project-a' },
      active_session_id: 'session-a',
      agent_sessions: [
        { id: 'session-b', title: '服务端次标题', updated_at: 300 },
        { id: 'session-a', title: '服务端首标题', updated_at: 200 },
      ],
      messages: [{ id: '不应合并' }],
      image_generations: [{ id: '不应合并' }],
    };

    const merged = mergeAuthoritativeSessionMetadata(
      current,
      authoritative,
      'project-a',
      'session-a',
    );

    expect(merged.agent_sessions).toEqual(authoritative.agent_sessions);
    expect(merged.agent_sessions.map((session) => session.id)).toEqual([
      'session-b',
      'session-a',
    ]);
    expect(merged.agent_sessions[1].title).toBe('服务端首标题');
    expect(merged.messages).toBe(messages);
    expect(merged.image_generations).toBe(imageGenerations);
  });

  test('does not overwrite newer session activity with an older response', () => {
    const current = {
      project: { id: 'project-a' },
      active_session_id: 'session-a',
      agent_sessions: [{ id: 'session-a', title: '较新标题', updated_at: 300 }],
    };
    const stale = {
      project: { id: 'project-a' },
      active_session_id: 'session-a',
      agent_sessions: [{ id: 'session-a', title: '较旧标题', updated_at: 200 }],
    };

    expect(
      mergeAuthoritativeSessionMetadata(
        current,
        stale,
        'project-a',
        'session-a',
      ),
    ).toBe(current);
  });
});
