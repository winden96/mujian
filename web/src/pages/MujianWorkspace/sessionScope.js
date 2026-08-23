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

export const generationMatchesScope = (generation, projectId, sessionId) =>
  Boolean(
    generation &&
      projectId &&
      sessionId &&
      generation.project_id === projectId &&
      generation.session_id === sessionId,
  );

export const hasWorkspaceDraft = ({
  message,
  attachments,
  imagePrompt,
  imageReferences,
}) =>
  Boolean(
    message.trim() ||
      attachments.length ||
      imagePrompt.trim() ||
      imageReferences.length,
  );

const sessionUpdatedAt = (session) => Number(session?.updated_at) || 0;

export const mergeAuthoritativeSessionMetadata = (
  current,
  authoritative,
  expectedProjectId,
  expectedSessionId,
) => {
  if (
    !expectedProjectId ||
    !expectedSessionId ||
    current?.project?.id !== expectedProjectId ||
    current.active_session_id !== expectedSessionId ||
    authoritative?.project?.id !== expectedProjectId ||
    authoritative.active_session_id !== expectedSessionId ||
    !Array.isArray(current.agent_sessions) ||
    !Array.isArray(authoritative.agent_sessions)
  ) {
    return current;
  }

  const authoritativeById = new Map(
    authoritative.agent_sessions.map((session) => [session.id, session]),
  );
  if (
    !current.agent_sessions.some(
      (session) => session.id === expectedSessionId,
    ) ||
    !authoritativeById.has(expectedSessionId)
  ) {
    return current;
  }

  const wouldOverwriteNewerMetadata = current.agent_sessions.some((session) => {
    const next = authoritativeById.get(session.id);
    return !next || sessionUpdatedAt(session) > sessionUpdatedAt(next);
  });
  if (wouldOverwriteNewerMetadata) return current;

  return {
    ...current,
    agent_sessions: authoritative.agent_sessions,
    active_session_id: authoritative.active_session_id,
  };
};
