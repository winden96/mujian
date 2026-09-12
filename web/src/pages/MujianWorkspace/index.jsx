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

import { mujianModelOption } from '../../helpers/mujianPricing';
import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { useParams, useSearchParams } from 'react-router-dom';
import { Modal, Spin } from '@douyinfe/semi-ui';
import { API, showError, showSuccess } from '../../helpers';
import {
  agentAttachmentDisplayContent,
  agentAttachmentPayload,
  maxAgentAttachments,
  maxAgentAttachmentTotalBytes,
  readAgentAttachment,
} from './attachments';
import WorkspaceImageLibrary from './ProjectResults';
import WorkspaceAgentPanel from './WorkspaceAgentPanel';
import {
  createImageGeneration,
  createImageReference,
  getImageGeneration,
  imageModelsByEngine,
  maxImageReferences,
  maxImageReferenceTotalBytes,
  regenerateImageGeneration,
  releaseImageReference,
} from './imageGenerations';
import { streamAgentMessage } from './agentStream';
import {
  clearAgentSession,
  createAgentSession,
  getAgentSessionWorkspace,
} from './sessions';
import {
  generationMatchesScope,
  hasWorkspaceDraft,
  mergeAuthoritativeSessionMetadata,
} from './sessionScope';
import '../mujian.css';
import './workspace.css';

const agentSkills = [
  { label: '短剧编剧', value: '短剧编剧' },
  { label: '分镜导演', value: '分镜导演' },
  { label: '画面提示词', value: '画面提示词' },
];
const imageEngines = [
  { label: 'Nano', value: 'nano' },
  { label: 'GPT', value: 'gpt' },
];
const wait = (milliseconds) =>
  new Promise((resolve) => window.setTimeout(resolve, milliseconds));
const generationPoll = { interval: 1600, maxAttempts: 300 };
const compactWorkspaceQuery = '(max-width: 1279px)';

const parseEnabledSkills = (value) => {
  if (Array.isArray(value)) return value;
  try {
    return JSON.parse(value || '[]');
  } catch {
    return [];
  }
};

const requestError = (error, fallback) =>
  error.response?.data?.message || error.message || fallback;

const assertWorkspaceScope = (workspace, projectId, sessionId) => {
  if (
    !sessionId ||
    workspace?.project?.id !== projectId ||
    workspace.active_session_id !== sessionId
  ) {
    throw new Error('会话响应不匹配，请重试');
  }
};

const mergeAgentSessionWorkspace = (current, next) => {
  if (current?.project?.id !== next.project.id) return current;
  return {
    ...current,
    agent_sessions: next.agent_sessions,
    active_session_id: next.active_session_id,
    messages: next.messages,
    image_generations: next.image_generations,
  };
};

const MujianWorkspace = () => {
  const { projectId } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const requestedSessionId = searchParams.get('session_id') || '';
  const [workspace, setWorkspace] = useState(null);
  const [preference, setPreference] = useState(null);
  const [models, setModels] = useState({ chat: [], image: [], items: [] });
  const [creationMode, setCreationMode] = useState('chat');
  const [currentSkill, setCurrentSkill] = useState(agentSkills[0].value);
  const [compactWorkspace, setCompactWorkspace] = useState(
    () => window.matchMedia(compactWorkspaceQuery).matches,
  );
  const [resultsOpen, setResultsOpen] = useState(
    () => !window.matchMedia(compactWorkspaceQuery).matches,
  );
  // null: session not initialized; empty string: user intentionally collapsed all.
  const [selectedGenerationId, setSelectedGenerationId] = useState(null);
  const [message, setMessage] = useState('');
  const [attachments, setAttachments] = useState([]);
  const [imagePrompt, setImagePrompt] = useState('');
  const [imageEngine, setImageEngine] = useState('nano');
  const [imageAspectRatio, setImageAspectRatio] = useState('9:16');
  const [imageReferences, setImageReferences] = useState([]);
  const [selectedImageModels, setSelectedImageModels] = useState({
    nano: '',
    gpt: '',
  });
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [imageSubmitting, setImageSubmitting] = useState(false);
  const [regeneratingIds, setRegeneratingIds] = useState(() => new Set());
  const [readingAttachments, setReadingAttachments] = useState(false);
  const [savingDefaults, setSavingDefaults] = useState({});
  const [sessionChanging, setSessionChanging] = useState('');
  const mounted = useRef(true);
  const generationRequests = useRef(new Map());
  const generationActionRequests = useRef(new Map());
  const imageCreateRequest = useRef(null);
  const agentRequest = useRef(null);
  const sessionRequest = useRef(null);
  const sessionMetadataRequest = useRef(null);
  const sessionConfirm = useRef(null);
  const pendingUrlSession = useRef('');
  const resultsToggleRef = useRef(null);
  const restoreResultsToggleFocus = useRef(false);
  const readingAttachmentsRef = useRef(false);
  const attachmentReadEpoch = useRef(0);
  const preferenceUpdates = useRef(new Set());
  const activeProjectIdRef = useRef(projectId);
  const activeSessionIdRef = useRef('');
  const imageReferencesRef = useRef(imageReferences);
  activeProjectIdRef.current = projectId;
  imageReferencesRef.current = imageReferences;

  const setSessionInUrl = useCallback(
    (sessionId, replace = false) => {
      const next = new URLSearchParams(window.location.search);
      if (sessionId) next.set('session_id', sessionId);
      else next.delete('session_id');
      setSearchParams(next, { replace });
    },
    [setSearchParams],
  );

  const abortSessionImageRequests = useCallback(() => {
    generationRequests.current.forEach((controller) => controller.abort());
    generationRequests.current.clear();
    generationActionRequests.current.forEach((controller) =>
      controller.abort(),
    );
    generationActionRequests.current.clear();
    imageCreateRequest.current?.abort();
    imageCreateRequest.current = null;
  }, []);

  const abortProjectRequests = useCallback(() => {
    abortSessionImageRequests();
    agentRequest.current?.abort();
    agentRequest.current = null;
    sessionRequest.current?.abort();
    sessionRequest.current = null;
    sessionMetadataRequest.current?.abort();
    sessionMetadataRequest.current = null;
    sessionConfirm.current?.destroy?.();
    sessionConfirm.current = null;
    pendingUrlSession.current = '';
  }, [abortSessionImageRequests]);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      abortProjectRequests();
      imageReferencesRef.current.forEach(releaseImageReference);
      attachmentReadEpoch.current += 1;
    };
  }, [abortProjectRequests]);

  useLayoutEffect(() => {
    abortProjectRequests();
    imageReferencesRef.current.forEach(releaseImageReference);
    attachmentReadEpoch.current += 1;
    readingAttachmentsRef.current = false;
    setLoading(true);
    setWorkspace(null);
    setCurrentSkill(agentSkills[0].value);
    setCreationMode('chat');
    restoreResultsToggleFocus.current = false;
    setResultsOpen(!window.matchMedia(compactWorkspaceQuery).matches);
    setSelectedGenerationId(null);
    setMessage('');
    setAttachments([]);
    setImagePrompt('');
    setImageReferences([]);
    setSending(false);
    setImageSubmitting(false);
    setRegeneratingIds(new Set());
    setReadingAttachments(false);
    setSessionChanging('');
  }, [abortProjectRequests, projectId]);

  useEffect(() => {
    const mediaQuery = window.matchMedia(compactWorkspaceQuery);
    const handleBreakpointChange = (event) => {
      const resultsPanel = document.getElementById('mujian-results-panel');
      restoreResultsToggleFocus.current = Boolean(
        event.matches &&
          (resultsPanel?.contains(document.activeElement) ||
            resultsPanel?.querySelector(
              '[aria-expanded="true"]:not(.mujian-generation-summary)',
            )),
      );
      setCompactWorkspace(event.matches);
      setResultsOpen(!event.matches);
    };
    mediaQuery.addEventListener('change', handleBreakpointChange);
    return () =>
      mediaQuery.removeEventListener('change', handleBreakpointChange);
  }, []);

  const load = useCallback(
    async (signal) => {
      if (mounted.current) setLoading(true);
      try {
        const initialSessionId = new URLSearchParams(
          window.location.search,
        ).get('session_id');
        const loadInitialWorkspace = async () => {
          try {
            return await getAgentSessionWorkspace(
              projectId,
              initialSessionId,
              signal,
            );
          } catch (error) {
            // A saved URL may point at a session that has since been removed.
            if (!initialSessionId || error.response?.status !== 404)
              throw error;
            return getAgentSessionWorkspace(projectId, '', signal);
          }
        };
        const [loadedWorkspace, preferenceResponse] = await Promise.all([
          loadInitialWorkspace(),
          API.get('/api/mujian/preferences', { signal }),
        ]);
        if (!preferenceResponse.data.success) {
          throw new Error(preferenceResponse.data.message);
        }
        assertWorkspaceScope(
          loadedWorkspace,
          projectId,
          loadedWorkspace.active_session_id,
        );
        if (mounted.current && activeProjectIdRef.current === projectId) {
          setWorkspace(loadedWorkspace);
          setPreference(preferenceResponse.data.data);
          setModels(
            preferenceResponse.data.models || {
              chat: [],
              image: [],
              items: [],
            },
          );
          if (
            loadedWorkspace.active_session_id &&
            loadedWorkspace.active_session_id !== initialSessionId
          ) {
            setSessionInUrl(loadedWorkspace.active_session_id, true);
          }
        }
      } catch (error) {
        if (
          mounted.current &&
          activeProjectIdRef.current === projectId &&
          error.code !== 'ERR_CANCELED'
        ) {
          showError(requestError(error, '工作区加载失败'));
        }
      } finally {
        if (mounted.current && activeProjectIdRef.current === projectId) {
          setLoading(false);
        }
      }
    },
    [projectId, setSessionInUrl],
  );

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const visibleWorkspace =
    workspace?.project?.id === projectId ? workspace : null;
  const projectMessages = useMemo(
    () =>
      (visibleWorkspace?.messages || [])
        .map((item, index) => ({ item, index }))
        .sort((left, right) => {
          const timestampDifference =
            (Number(left.item.created_at) || 0) -
            (Number(right.item.created_at) || 0);
          return timestampDifference || left.index - right.index;
        })
        .map(({ item }) => item),
    [visibleWorkspace],
  );
  const projectGenerations = useMemo(
    () => visibleWorkspace?.image_generations || [],
    [visibleWorkspace],
  );
  const agentSessions = useMemo(
    () => visibleWorkspace?.agent_sessions || [],
    [visibleWorkspace],
  );
  const activeSessionId = visibleWorkspace?.active_session_id || '';
  activeSessionIdRef.current = activeSessionId;

  const discardWorkspaceDraft = useCallback(() => {
    attachmentReadEpoch.current += 1;
    readingAttachmentsRef.current = false;
    setReadingAttachments(false);
    setMessage('');
    setAttachments([]);
    setImagePrompt('');
    imageReferencesRef.current.forEach(releaseImageReference);
    imageReferencesRef.current = [];
    setImageReferences([]);
  }, []);

  const switchSession = useCallback(
    async (sessionId, urlAlreadyChanged = false) => {
      if (
        !sessionId ||
        sessionId === activeSessionIdRef.current ||
        sessionRequest.current ||
        agentRequest.current ||
        imageCreateRequest.current ||
        generationActionRequests.current.size > 0 ||
        readingAttachmentsRef.current
      ) {
        return;
      }

      const startedProjectId = projectId;
      const controller = new AbortController();
      sessionRequest.current = controller;
      setSessionChanging('switching');
      try {
        const nextWorkspace = await getAgentSessionWorkspace(
          projectId,
          sessionId,
          controller.signal,
        );
        assertWorkspaceScope(nextWorkspace, startedProjectId, sessionId);
        if (
          mounted.current &&
          activeProjectIdRef.current === startedProjectId &&
          sessionRequest.current === controller
        ) {
          activeSessionIdRef.current = sessionId;
          abortSessionImageRequests();
          setSelectedGenerationId(null);
          setWorkspace((current) =>
            mergeAgentSessionWorkspace(current, nextWorkspace),
          );
          discardWorkspaceDraft();
          if (!urlAlreadyChanged) setSessionInUrl(sessionId);
        }
      } catch (error) {
        if (
          mounted.current &&
          activeProjectIdRef.current === startedProjectId &&
          sessionRequest.current === controller &&
          error.code !== 'ERR_CANCELED'
        ) {
          if (urlAlreadyChanged) {
            setSessionInUrl(activeSessionIdRef.current);
          }
          showError(requestError(error, '会话切换失败'));
        }
      } finally {
        if (sessionRequest.current === controller) {
          sessionRequest.current = null;
          if (mounted.current) setSessionChanging('');
        }
      }
    },
    [
      abortSessionImageRequests,
      discardWorkspaceDraft,
      projectId,
      setSessionInUrl,
    ],
  );

  const createSession = useCallback(async () => {
    if (
      !activeSessionIdRef.current ||
      sessionRequest.current ||
      agentRequest.current ||
      imageCreateRequest.current ||
      generationActionRequests.current.size > 0 ||
      readingAttachmentsRef.current
    ) {
      return;
    }

    const startedProjectId = projectId;
    const controller = new AbortController();
    sessionRequest.current = controller;
    setSessionChanging('creating');
    try {
      const nextWorkspace = await createAgentSession(
        projectId,
        controller.signal,
      );
      const nextSessionId = nextWorkspace.active_session_id;
      if (!nextSessionId) throw new Error('新会话创建响应无效');
      assertWorkspaceScope(nextWorkspace, startedProjectId, nextSessionId);
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        sessionRequest.current === controller
      ) {
        activeSessionIdRef.current = nextSessionId;
        abortSessionImageRequests();
        setSelectedGenerationId(null);
        setWorkspace((current) =>
          mergeAgentSessionWorkspace(current, nextWorkspace),
        );
        discardWorkspaceDraft();
        setSessionInUrl(nextSessionId);
        showSuccess('已新建会话');
      }
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        sessionRequest.current === controller &&
        error.code !== 'ERR_CANCELED'
      ) {
        showError(requestError(error, '新建会话失败'));
      }
    } finally {
      if (sessionRequest.current === controller) {
        sessionRequest.current = null;
        if (mounted.current) setSessionChanging('');
      }
    }
  }, [
    abortSessionImageRequests,
    discardWorkspaceDraft,
    projectId,
    setSessionInUrl,
  ]);

  const clearSession = useCallback(async () => {
    const sessionId = activeSessionIdRef.current;
    if (
      !sessionId ||
      sessionRequest.current ||
      agentRequest.current ||
      imageCreateRequest.current ||
      generationActionRequests.current.size > 0 ||
      readingAttachmentsRef.current
    ) {
      return;
    }

    const startedProjectId = projectId;
    const controller = new AbortController();
    sessionRequest.current = controller;
    setSessionChanging('clearing');
    try {
      const nextWorkspace = await clearAgentSession(
        projectId,
        sessionId,
        controller.signal,
      );
      assertWorkspaceScope(nextWorkspace, startedProjectId, sessionId);
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        sessionRequest.current === controller
      ) {
        setWorkspace((current) =>
          mergeAgentSessionWorkspace(current, nextWorkspace),
        );
        showSuccess('已清空当前会话');
      }
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        sessionRequest.current === controller &&
        error.code !== 'ERR_CANCELED'
      ) {
        showError(requestError(error, '清空会话失败'));
      }
    } finally {
      if (sessionRequest.current === controller) {
        sessionRequest.current = null;
        if (mounted.current) setSessionChanging('');
      }
    }
  }, [projectId]);

  const confirmDraftDiscard = useCallback(
    (actionLabel, onConfirm, onCancel) => {
      if (
        !hasWorkspaceDraft({
          message,
          attachments,
          imagePrompt,
          imageReferences,
        })
      ) {
        return onConfirm();
      }
      sessionConfirm.current?.destroy?.();
      sessionConfirm.current = Modal.confirm({
        title: `放弃草稿并${actionLabel}？`,
        content:
          '当前未发送的文字、附件、图片提示词和参考图将丢失，已保存的内容不受影响。',
        okText: '放弃并继续',
        cancelText: '取消',
        onOk: async () => {
          try {
            await onConfirm();
          } finally {
            sessionConfirm.current = null;
          }
        },
        onCancel: () => {
          sessionConfirm.current = null;
          onCancel?.();
        },
      });
      return undefined;
    },
    [attachments, imagePrompt, imageReferences, message],
  );

  const requestSessionSwitch = useCallback(
    (sessionId) => {
      if (!sessionId || sessionId === activeSessionIdRef.current) return;
      confirmDraftDiscard('切换会话', () => switchSession(sessionId));
    },
    [confirmDraftDiscard, switchSession],
  );

  const requestNewSession = useCallback(() => {
    confirmDraftDiscard('新建会话', createSession);
  }, [confirmDraftDiscard, createSession]);

  const requestClearSession = useCallback(() => {
    if (!activeSessionIdRef.current) return;
    sessionConfirm.current?.destroy?.();
    sessionConfirm.current = Modal.confirm({
      title: '永久清空当前会话的聊天？',
      content:
        '当前会话的全部聊天记录将被永久删除，无法恢复；已生成图片会保留。',
      okText: '永久清空',
      cancelText: '取消',
      okButtonProps: { type: 'danger' },
      onOk: async () => {
        try {
          await clearSession();
        } finally {
          sessionConfirm.current = null;
        }
      },
      onCancel: () => {
        sessionConfirm.current = null;
      },
    });
  }, [clearSession]);

  useEffect(() => {
    if (!visibleWorkspace || !activeSessionId) return;
    if (
      pendingUrlSession.current &&
      pendingUrlSession.current !== requestedSessionId
    ) {
      sessionConfirm.current?.destroy?.();
      sessionConfirm.current = null;
      sessionRequest.current?.abort();
      sessionRequest.current = null;
      pendingUrlSession.current = '';
      setSessionChanging('');
    }
    if (!requestedSessionId) {
      setSessionInUrl(activeSessionId, true);
      return;
    }
    if (requestedSessionId === activeSessionId) return;
    if (!agentSessions.some((session) => session.id === requestedSessionId)) {
      setSessionInUrl(activeSessionId, true);
      return;
    }
    if (pendingUrlSession.current === requestedSessionId) return;
    if (
      agentRequest.current ||
      imageCreateRequest.current ||
      generationActionRequests.current.size > 0 ||
      sessionRequest.current ||
      readingAttachmentsRef.current
    ) {
      setSessionInUrl(activeSessionId);
      return;
    }

    pendingUrlSession.current = requestedSessionId;
    const finish = () => {
      pendingUrlSession.current = '';
    };
    confirmDraftDiscard(
      '切换会话',
      async () => {
        await switchSession(requestedSessionId, true);
        finish();
      },
      () => {
        setSessionInUrl(activeSessionId);
        finish();
      },
    );
  }, [
    activeSessionId,
    agentSessions,
    confirmDraftDiscard,
    requestedSessionId,
    setSessionInUrl,
    switchSession,
    visibleWorkspace,
  ]);

  useEffect(() => {
    if (!activeSessionId) return;
    setSelectedGenerationId((current) => {
      if (current === '') return current;
      if (
        current !== null &&
        projectGenerations.some(
          (generation) => String(generation.id) === String(current),
        )
      ) {
        return current;
      }
      const latest = projectGenerations.reduce((result, generation) => {
        const createdAt = Number(generation.created_at) || 0;
        if (result == null || createdAt > result.createdAt) {
          return { id: generation.id, createdAt };
        }
        return result;
      }, null);
      return latest?.id ?? null;
    });
  }, [activeSessionId, projectGenerations]);

  const imageModels = useMemo(
    () => imageModelsByEngine(models.image),
    [models.image],
  );
  const imageCapabilities = useMemo(
    () =>
      new Map(
        (models.items || [])
          .filter((item) => item.kind === 'image')
          .map((item) => [item.id, item]),
      ),
    [models.items],
  );
  const hasChatModels = models.chat.length > 0;
  const enabledSkills = useMemo(
    () => parseEnabledSkills(preference?.enabled_skills),
    [preference?.enabled_skills],
  );
  const skillEnabled = enabledSkills.includes(currentSkill);
  const chatModelAvailable = models.chat.includes(
    preference?.default_chat_model,
  );
  const hasImageReferences = imageReferences.length > 0;
  const compatibleImageModels = useMemo(() => {
    const result = {};
    imageEngines.forEach(({ value: engine }) => {
      result[engine] = hasImageReferences
        ? imageModels[engine].filter(
            (model) => imageCapabilities.get(model)?.reference_available,
          )
        : imageModels[engine];
    });
    return result;
  }, [hasImageReferences, imageCapabilities, imageModels]);

  useEffect(() => {
    setSelectedImageModels((current) => {
      const next = { ...current };
      imageEngines.forEach(({ value: engine }) => {
        const compatibleCandidates = compatibleImageModels[engine];
        const preferred = compatibleCandidates.includes(
          preference?.default_image_model,
        )
          ? preference.default_image_model
          : '';
        if (!compatibleCandidates.includes(next[engine])) {
          next[engine] = preferred || compatibleCandidates[0] || '';
        }
      });
      return next.nano === current.nano && next.gpt === current.gpt
        ? current
        : next;
    });
  }, [compatibleImageModels, preference?.default_image_model]);

  const selectedImageModel = selectedImageModels[imageEngine];
  const familyImageModels = imageModels[imageEngine];
  const selectedImageCapability = imageCapabilities.get(selectedImageModel);
  const selectedImageModelAvailable =
    familyImageModels.includes(selectedImageModel) &&
    (!hasImageReferences || selectedImageCapability?.reference_available);
  const referenceCapableModels = familyImageModels.filter(
    (model) => imageCapabilities.get(model)?.reference_available,
  );
  const referenceUnavailableReason =
    familyImageModels
      .map(
        (model) => imageCapabilities.get(model)?.reference_unavailable_reason,
      )
      .find(Boolean) || '当前引擎没有已验证的多图参考渠道。';

  const updateDefault = async (field, value) => {
    if (preferenceUpdates.current.has(field)) return;
    preferenceUpdates.current.add(field);
    setSavingDefaults((current) => ({ ...current, [field]: true }));
    try {
      const response = await API.put('/api/mujian/preferences', {
        [field]: value,
      });
      if (!response.data.success) throw new Error(response.data.message);
      if (mounted.current) {
        setPreference((current) => ({
          ...current,
          [field]: response.data.data[field],
        }));
      }
    } catch (error) {
      if (mounted.current) showError(requestError(error, '模型切换失败'));
    } finally {
      preferenceUpdates.current.delete(field);
      if (mounted.current) {
        setSavingDefaults((current) => ({ ...current, [field]: false }));
      }
    }
  };

  const addAttachments = async (event) => {
    if (
      readingAttachmentsRef.current ||
      agentRequest.current ||
      sessionRequest.current
    ) {
      event.target.value = '';
      return;
    }
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (!files.length) return;
    if (attachments.length + files.length > maxAgentAttachments) {
      showError(`每次最多上传 ${maxAgentAttachments} 个附件`);
      return;
    }
    const totalBytes = [...attachments, ...files].reduce(
      (total, item) => total + item.size,
      0,
    );
    if (totalBytes > maxAgentAttachmentTotalBytes) {
      showError('附件总大小不能超过 20MB');
      return;
    }
    const startedProjectId = projectId;
    const readEpoch = attachmentReadEpoch.current + 1;
    attachmentReadEpoch.current = readEpoch;
    readingAttachmentsRef.current = true;
    setReadingAttachments(true);
    try {
      const nextAttachments = await Promise.all(files.map(readAgentAttachment));
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        attachmentReadEpoch.current === readEpoch
      ) {
        setAttachments((current) => [...current, ...nextAttachments]);
      }
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        attachmentReadEpoch.current === readEpoch
      ) {
        showError(error.message || '附件读取失败');
      }
    } finally {
      if (attachmentReadEpoch.current === readEpoch) {
        readingAttachmentsRef.current = false;
        if (mounted.current) setReadingAttachments(false);
      }
    }
  };

  const addImageReferences = (event) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    if (imageCreateRequest.current || sessionRequest.current) return;
    if (!files.length) return;
    if (imageReferences.length + files.length > maxImageReferences) {
      showError(`最多添加 ${maxImageReferences} 张参考图`);
      return;
    }
    const totalBytes = [...imageReferences, ...files].reduce(
      (total, item) => total + item.size,
      0,
    );
    if (totalBytes > maxImageReferenceTotalBytes) {
      showError('参考图总大小不能超过 14MB');
      return;
    }
    const next = [];
    try {
      files.forEach((file) => next.push(createImageReference(file)));
      setImageReferences((current) => [...current, ...next]);
      if (!selectedImageCapability?.reference_available) {
        const compatibleModel = referenceCapableModels[0];
        if (compatibleModel) {
          setSelectedImageModels((current) => ({
            ...current,
            [imageEngine]: compatibleModel,
          }));
          showSuccess(`已切换至支持多图参考的 ${compatibleModel}`);
        }
      }
    } catch (error) {
      next.forEach(releaseImageReference);
      showError(error.message || '参考图读取失败');
    }
  };

  const removeImageReference = (referenceId) => {
    if (imageCreateRequest.current || sessionRequest.current) return;
    setImageReferences((current) => {
      const removed = current.find((item) => item.id === referenceId);
      releaseImageReference(removed);
      return current.filter((item) => item.id !== referenceId);
    });
  };

  const refreshAgentSessionMetadata = useCallback(
    async (expectedProjectId, expectedSessionId) => {
      sessionMetadataRequest.current?.abort();
      const controller = new AbortController();
      sessionMetadataRequest.current = controller;
      try {
        const authoritative = await getAgentSessionWorkspace(
          expectedProjectId,
          expectedSessionId,
          controller.signal,
        );
        if (
          mounted.current &&
          activeProjectIdRef.current === expectedProjectId &&
          activeSessionIdRef.current === expectedSessionId &&
          sessionMetadataRequest.current === controller
        ) {
          setWorkspace((current) =>
            mergeAuthoritativeSessionMetadata(
              current,
              authoritative,
              expectedProjectId,
              expectedSessionId,
            ),
          );
        }
      } catch {
        // The completed primary action stays successful when metadata refresh fails.
      } finally {
        if (sessionMetadataRequest.current === controller) {
          sessionMetadataRequest.current = null;
        }
      }
    },
    [],
  );

  const sendMessage = async () => {
    const content = message.trim();
    const startedSessionId = activeSessionIdRef.current;
    if (
      (!content && attachments.length === 0) ||
      !startedSessionId ||
      !skillEnabled ||
      readingAttachmentsRef.current ||
      agentRequest.current ||
      imageCreateRequest.current ||
      sessionRequest.current ||
      preferenceUpdates.current.has('default_chat_model')
    ) {
      return;
    }
    const startedProjectId = projectId;
    const requestAttachments = attachments;
    const displayContent = agentAttachmentDisplayContent(
      content,
      requestAttachments,
    );
    const request = new AbortController();
    agentRequest.current = request;
    const requestKey = window.crypto.randomUUID();
    const userMessageId = `stream-user-${requestKey}`;
    const assistantMessageId = `stream-assistant-${requestKey}`;
    const createdAt = Date.now();
    setSending(true);
    setMessage('');
    setAttachments([]);
    setWorkspace((current) =>
      current?.active_session_id === startedSessionId
        ? {
            ...current,
            messages: [
              ...(current.messages || []),
              {
                id: userMessageId,
                role: 'user',
                session_id: startedSessionId,
                content: displayContent,
                skill: currentSkill,
                created_at: createdAt,
              },
              {
                id: assistantMessageId,
                role: 'assistant',
                session_id: startedSessionId,
                content: '',
                skill: currentSkill,
                streaming: true,
                created_at: createdAt,
              },
            ],
          }
        : current,
    );
    try {
      await streamAgentMessage({
        projectId,
        signal: request.signal,
        payload: {
          content,
          session_id: startedSessionId,
          skill: currentSkill,
          model_id: preference.default_chat_model,
          attachments: agentAttachmentPayload(requestAttachments),
        },
        onEvent: ({ type, data }) => {
          if (
            data.session_id !== undefined &&
            data.session_id !== startedSessionId
          ) {
            throw new Error('收到了其他会话的响应，请重试');
          }
          if (
            (type === 'started' || type === 'completed') &&
            !data.session_id
          ) {
            throw new Error('Agent 响应缺少会话标识，请重试');
          }
          if (
            type === 'completed' &&
            (data.user_message?.project_id !== startedProjectId ||
              data.message?.project_id !== startedProjectId ||
              data.user_message?.session_id !== startedSessionId ||
              data.message?.session_id !== startedSessionId ||
              !Number.isFinite(Number(data.message?.created_at)))
          ) {
            throw new Error('Agent 完成响应不匹配，请重试');
          }
          if (
            !mounted.current ||
            activeProjectIdRef.current !== startedProjectId ||
            activeSessionIdRef.current !== startedSessionId ||
            agentRequest.current !== request
          ) {
            return;
          }
          if (type === 'started' && data.reasoning_requested === true) {
            setWorkspace((current) =>
              current?.active_session_id === startedSessionId
                ? {
                    ...current,
                    messages: current.messages.map((item) =>
                      item.id === assistantMessageId
                        ? { ...item, reasoning: '' }
                        : item,
                    ),
                  }
                : current,
            );
          }
          if (type === 'delta') {
            setWorkspace((current) =>
              current?.active_session_id === startedSessionId
                ? {
                    ...current,
                    messages: current.messages.map((item) =>
                      item.id === assistantMessageId
                        ? { ...item, content: item.content + data.text }
                        : item,
                    ),
                  }
                : current,
            );
          }
          if (type === 'reasoning_delta') {
            setWorkspace((current) =>
              current?.active_session_id === startedSessionId
                ? {
                    ...current,
                    messages: current.messages.map((item) =>
                      item.id === assistantMessageId
                        ? {
                            ...item,
                            reasoning: (item.reasoning || '') + data.text,
                          }
                        : item,
                    ),
                  }
                : current,
            );
          }
          if (type === 'completed') {
            setWorkspace((current) => {
              if (current?.active_session_id !== startedSessionId) {
                return current;
              }
              return {
                ...current,
                messages: current.messages.map((item) => {
                  if (item.id === userMessageId) return data.user_message;
                  if (item.id === assistantMessageId) return data.message;
                  return item;
                }),
              };
            });
            void refreshAgentSessionMetadata(
              startedProjectId,
              startedSessionId,
            );
          }
        },
      });
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        activeSessionIdRef.current === startedSessionId &&
        agentRequest.current === request &&
        error.name !== 'AbortError'
      ) {
        setWorkspace((current) =>
          current?.active_session_id === startedSessionId
            ? {
                ...current,
                messages: current.messages.filter(
                  (item) =>
                    item.id !== userMessageId && item.id !== assistantMessageId,
                ),
              }
            : current,
        );
        setMessage(content);
        setAttachments(requestAttachments);
        showError(error.message || 'Agent 调用失败');
      }
    } finally {
      if (agentRequest.current === request) {
        agentRequest.current = null;
        if (mounted.current) setSending(false);
      }
    }
  };

  const updateGeneration = useCallback(
    (generation, expectedProjectId, expectedSessionId) => {
      if (
        !generationMatchesScope(
          generation,
          expectedProjectId,
          expectedSessionId,
        )
      ) {
        throw new Error('图片任务响应不匹配，请重试');
      }
      setWorkspace((current) => {
        if (
          current?.project?.id !== expectedProjectId ||
          current.active_session_id !== expectedSessionId
        ) {
          return current;
        }
        const generations = current.image_generations || [];
        const exists = generations.some((item) => item.id === generation.id);
        return {
          ...current,
          image_generations: exists
            ? generations.map((item) =>
                item.id === generation.id ? generation : item,
              )
            : [...generations, generation],
        };
      });
    },
    [],
  );

  const pollGeneration = useCallback(
    async (expectedProjectId, expectedSessionId, generationId, controller) => {
      let lastTransientError;
      for (
        let attempt = 0;
        attempt < generationPoll.maxAttempts;
        attempt += 1
      ) {
        await wait(generationPoll.interval);
        let generation;
        try {
          generation = await getImageGeneration(
            expectedProjectId,
            expectedSessionId,
            generationId,
            controller.signal,
          );
        } catch (error) {
          if (error.code === 'ERR_CANCELED') throw error;
          const status = error.response?.status;
          if (status && status < 500) throw error;
          lastTransientError = error;
          continue;
        }
        updateGeneration(generation, expectedProjectId, expectedSessionId);
        if (generation.status === 'succeeded') {
          return generation;
        }
        if (generation.status === 'failed') return generation;
      }
      throw lastTransientError || new Error('生成仍在进行，请稍后刷新查看');
    },
    [updateGeneration],
  );

  useEffect(() => {
    if (!activeSessionId) return;
    projectGenerations.forEach((generation) => {
      const requestKey = `${activeSessionId}:${generation.id}`;
      if (
        !['queued', 'running'].includes(generation.status) ||
        generationRequests.current.has(requestKey)
      ) {
        return;
      }
      const controller = new AbortController();
      generationRequests.current.set(requestKey, controller);
      pollGeneration(projectId, activeSessionId, generation.id, controller)
        .then((completed) => {
          if (
            completed.status === 'succeeded' &&
            mounted.current &&
            activeProjectIdRef.current === projectId &&
            activeSessionIdRef.current === activeSessionId
          ) {
            showSuccess('画面已生成');
          }
        })
        .catch((error) => {
          if (
            mounted.current &&
            activeProjectIdRef.current === projectId &&
            activeSessionIdRef.current === activeSessionId &&
            error.code !== 'ERR_CANCELED'
          ) {
            showError(requestError(error, '生成状态同步失败'));
          }
        })
        .finally(() => {
          if (generationRequests.current.get(requestKey) === controller) {
            generationRequests.current.delete(requestKey);
          }
        });
    });
  }, [activeSessionId, pollGeneration, projectGenerations, projectId]);

  const submitImageGeneration = async () => {
    const prompt = imagePrompt.trim();
    const startedSessionId = activeSessionIdRef.current;
    if (
      !prompt ||
      !startedSessionId ||
      !selectedImageModelAvailable ||
      imageSubmitting ||
      agentRequest.current ||
      sessionRequest.current ||
      imageCreateRequest.current
    ) {
      return;
    }
    const startedProjectId = projectId;
    const submittedReferences = imageReferences;
    const controller = new AbortController();
    imageCreateRequest.current = controller;
    setImageSubmitting(true);
    try {
      const generation = await createImageGeneration(
        projectId,
        startedSessionId,
        {
          prompt,
          engine: imageEngine,
          modelId: selectedImageModel,
          aspectRatio: imageAspectRatio,
          references: submittedReferences,
        },
        controller.signal,
      );
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        activeSessionIdRef.current === startedSessionId &&
        imageCreateRequest.current === controller
      ) {
        updateGeneration(generation, startedProjectId, startedSessionId);
        void refreshAgentSessionMetadata(startedProjectId, startedSessionId);
        setSelectedGenerationId(generation.id);
        setResultsOpen(true);
        setImagePrompt('');
        const submittedIds = new Set(
          submittedReferences.map((reference) => reference.id),
        );
        submittedReferences.forEach(releaseImageReference);
        setImageReferences((current) =>
          current.filter((reference) => !submittedIds.has(reference.id)),
        );
        showSuccess('图片任务已提交');
      }
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        activeSessionIdRef.current === startedSessionId &&
        error.code !== 'ERR_CANCELED'
      ) {
        showError(requestError(error, '图片任务提交失败'));
      }
    } finally {
      if (imageCreateRequest.current === controller) {
        imageCreateRequest.current = null;
        if (mounted.current) setImageSubmitting(false);
      }
    }
  };

  const regenerateGeneration = async (generationId) => {
    const startedProjectId = projectId;
    const startedSessionId = activeSessionIdRef.current;
    if (!startedSessionId || sessionRequest.current) return;
    const requestKey = `${startedSessionId}:${generationId}`;
    if (generationActionRequests.current.has(requestKey)) return;
    const controller = new AbortController();
    generationActionRequests.current.set(requestKey, controller);
    setRegeneratingIds((current) => new Set(current).add(generationId));
    try {
      const generation = await regenerateImageGeneration(
        projectId,
        startedSessionId,
        generationId,
        controller.signal,
      );
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        activeSessionIdRef.current === startedSessionId &&
        generationActionRequests.current.get(requestKey) === controller
      ) {
        updateGeneration(generation, startedProjectId, startedSessionId);
        void refreshAgentSessionMetadata(startedProjectId, startedSessionId);
        setSelectedGenerationId(generation.id);
        setResultsOpen(true);
        showSuccess('已创建新的生成任务');
      }
    } catch (error) {
      if (
        mounted.current &&
        activeProjectIdRef.current === startedProjectId &&
        activeSessionIdRef.current === startedSessionId &&
        generationActionRequests.current.get(requestKey) === controller &&
        error.code !== 'ERR_CANCELED'
      ) {
        showError(requestError(error, '重新生成失败'));
      }
    } finally {
      if (generationActionRequests.current.get(requestKey) === controller) {
        generationActionRequests.current.delete(requestKey);
        if (mounted.current) {
          setRegeneratingIds((current) => {
            const next = new Set(current);
            next.delete(generationId);
            return next;
          });
        }
      }
    }
  };

  const changeImageEngine = (engine) => {
    const candidates = compatibleImageModels[engine];
    if (candidates.length === 0) return;
    setImageEngine(engine);
    if (!candidates.includes(selectedImageModels[engine])) {
      setSelectedImageModels((current) => ({
        ...current,
        [engine]: candidates[0],
      }));
    }
  };

  const closeResults = useCallback(() => {
    restoreResultsToggleFocus.current = true;
    setResultsOpen(false);
  }, []);

  useEffect(() => {
    if (!resultsOpen && restoreResultsToggleFocus.current) {
      restoreResultsToggleFocus.current = false;
      resultsToggleRef.current
        ?.querySelector('button')
        ?.focus({ preventScroll: true });
    }
  }, [resultsOpen]);

  const toggleResults = useCallback(() => {
    if (resultsOpen) {
      closeResults();
    } else {
      restoreResultsToggleFocus.current = false;
      setResultsOpen(true);
    }
  }, [closeResults, resultsOpen]);

  const imageLibraryBusy = projectGenerations.some((generation) =>
    ['queued', 'running'].includes(generation.status),
  );
  const sessionControlsBusy = Boolean(
    loading ||
      !visibleWorkspace ||
      sessionChanging ||
      sending ||
      imageSubmitting ||
      regeneratingIds.size > 0 ||
      readingAttachments,
  );

  return (
    <Spin spinning={loading} wrapperClassName='mujian-workspace-spin'>
      <main
        className={`mujian-workspace${resultsOpen ? '' : ' is-results-closed'}`}
      >
        <WorkspaceAgentPanel
          key={projectId}
          creationMode={creationMode}
          messages={projectMessages}
          projectTitle={visibleWorkspace?.project?.title || '创作工作区'}
          episode={visibleWorkspace?.project?.current_episode || 1}
          resultsOpen={resultsOpen}
          resultCount={projectGenerations.length}
          resultsBusy={imageLibraryBusy}
          resultsToggleRef={resultsToggleRef}
          currentSkill={currentSkill}
          currentModel={preference?.default_chat_model || ''}
          sending={sending}
          sessions={agentSessions}
          activeSessionId={activeSessionId}
          sessionChanging={sessionChanging}
          sessionControlsBusy={sessionControlsBusy}
          canClearSession={projectMessages.length > 0}
          onCreationModeChange={setCreationMode}
          onToggleResults={toggleResults}
          onSessionChange={requestSessionSwitch}
          onNewSession={requestNewSession}
          onClearSession={requestClearSession}
          chat={{
            value: message,
            attachments,
            skill: currentSkill,
            modelId: chatModelAvailable ? preference?.default_chat_model : '',
            modelOptions: models.chat.map((id) =>
              mujianModelOption(id, models.items),
            ),
            skillOptions: agentSkills,
            enabledSkills,
            hasModels: hasChatModels,
            modelAvailable: chatModelAvailable,
            skillEnabled,
            readingAttachments,
            savingModel: Boolean(savingDefaults.default_chat_model),
            onValueChange: setMessage,
            onAttachmentsChange: addAttachments,
            onRemoveAttachment: (attachmentId) =>
              setAttachments((current) =>
                current.filter((item) => item.id !== attachmentId),
              ),
            onSkillChange: setCurrentSkill,
            onModelChange: (value) =>
              updateDefault('default_chat_model', value),
            onChooseStarter: setMessage,
            onSubmit: sendMessage,
          }}
          image={{
            value: imagePrompt,
            engine: imageEngine,
            aspectRatio: imageAspectRatio,
            references: imageReferences,
            selectedModel: selectedImageModel,
            selectedModelAvailable: selectedImageModelAvailable,
            engineOptions: imageEngines.map((engine) => ({
              ...engine,
              disabled: compatibleImageModels[engine.value].length === 0,
            })),
            modelOptions: familyImageModels.map((value) => ({
              ...mujianModelOption(value, models.items),
              disabled:
                hasImageReferences &&
                !imageCapabilities.get(value)?.reference_available,
            })),
            hasFamilyModels: familyImageModels.length > 0,
            referenceUnavailableReason:
              hasImageReferences && referenceCapableModels.length === 0
                ? referenceUnavailableReason
                : '',
            submitting: imageSubmitting,
            interactionDisabled: Boolean(sessionChanging || sending),
            savingModel: Boolean(savingDefaults.default_image_model),
            maxReferences: maxImageReferences,
            onValueChange: setImagePrompt,
            onReferencesChange: addImageReferences,
            onRemoveReference: removeImageReference,
            onEngineChange: changeImageEngine,
            onModelChange: (value) => {
              setSelectedImageModels((current) => ({
                ...current,
                [imageEngine]: value,
              }));
              updateDefault('default_image_model', value);
            },
            onAspectRatioChange: setImageAspectRatio,
            onChooseStarter: setImagePrompt,
            onSubmit: submitImageGeneration,
          }}
        />

        {resultsOpen && (
          <button
            type='button'
            className='mujian-results-backdrop'
            aria-label='关闭当前会话图片库'
            tabIndex={-1}
            onClick={closeResults}
          />
        )}

        <WorkspaceImageLibrary
          key={`${projectId}:${activeSessionId}`}
          open={resultsOpen}
          modal={compactWorkspace}
          sessionId={activeSessionId}
          generations={projectGenerations}
          regeneratingIds={regeneratingIds}
          actionsDisabled={Boolean(sessionChanging)}
          selectedGenerationId={selectedGenerationId}
          onSelectGeneration={setSelectedGenerationId}
          onRegenerate={regenerateGeneration}
          onClose={closeResults}
        />
      </main>
    </Spin>
  );
};

export default MujianWorkspace;
