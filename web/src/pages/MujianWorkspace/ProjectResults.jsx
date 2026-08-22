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

import React, { useEffect, useMemo, useRef } from 'react';
import { Tag } from '@douyinfe/semi-ui';
import { ChevronDown, Images, PanelRightClose } from 'lucide-react';
import ImageGenerationCard from './ImageGenerationCard';

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',');
const emptyIds = new Set();

const generationStatusMeta = {
  queued: { label: '排队中', color: 'grey' },
  running: { label: '生成中', color: 'blue' },
  succeeded: { label: '已生成', color: 'green' },
  failed: { label: '生成失败', color: 'red' },
};

const generationTimestamp = (createdAt) => {
  if (createdAt == null || createdAt === '') return 0;
  const numeric = Number(createdAt);
  if (Number.isFinite(numeric)) {
    return numeric > 0 && numeric < 1000000000000 ? numeric * 1000 : numeric;
  }
  const parsed = Date.parse(createdAt);
  return Number.isNaN(parsed) ? 0 : parsed;
};

const generationDateTime = (createdAt) => {
  const timestamp = generationTimestamp(createdAt);
  if (!timestamp) return undefined;
  const date = new Date(timestamp);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
};

const formatGenerationTime = (createdAt) => {
  const timestamp = generationTimestamp(createdAt);
  if (!timestamp) return '时间未知';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(timestamp);
};

const WorkspaceImageLibrary = ({
  open = true,
  modal = false,
  sessionId,
  generations = [],
  regeneratingIds = emptyIds,
  actionsDisabled = false,
  selectedGenerationId = '',
  onSelectGeneration,
  onRegenerate,
  onClose,
}) => {
  const panelRef = useRef(null);
  const closeButtonRef = useRef(null);
  const previousOpenRef = useRef(open);
  const sortedGenerations = useMemo(
    () =>
      generations
        .map((generation, index) => ({ generation, index }))
        .sort((left, right) => {
          const timestampDifference =
            generationTimestamp(right.generation.created_at) -
            generationTimestamp(left.generation.created_at);
          if (timestampDifference) return timestampDifference;
          return left.index - right.index;
        })
        .map(({ generation }) => generation),
    [generations],
  );
  const busy = generations.some((generation) =>
    ['queued', 'running'].includes(generation.status),
  );

  useEffect(() => {
    const wasOpen = previousOpenRef.current;
    previousOpenRef.current = open;
    if (!open || !modal) return undefined;

    let focusFrame;
    if (!wasOpen) {
      focusFrame = window.requestAnimationFrame(() => {
        closeButtonRef.current?.focus({ preventScroll: true });
      });
    }

    const handleKeyDown = (event) => {
      if (event.defaultPrevented) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose?.();
        return;
      }
      if (event.key !== 'Tab') return;

      const panel = panelRef.current;
      if (!panel) return;
      const focusableElements = Array.from(
        panel.querySelectorAll(focusableSelector),
      ).filter((element) => element.getClientRects().length > 0);
      if (focusableElements.length === 0) return;

      const first = focusableElements[0];
      const last = focusableElements[focusableElements.length - 1];
      const focusIsOutside = !panel.contains(document.activeElement);
      if (
        event.shiftKey &&
        (document.activeElement === first || focusIsOutside)
      ) {
        event.preventDefault();
        last.focus();
      } else if (
        !event.shiftKey &&
        (document.activeElement === last || focusIsOutside)
      ) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (focusFrame) window.cancelAnimationFrame(focusFrame);
    };
  }, [modal, onClose, open]);

  return (
    <aside
      ref={panelRef}
      id='mujian-results-panel'
      className={`mujian-project-results mujian-results-panel mujian-image-library ${open ? 'is-open' : 'is-closed'}`}
      aria-labelledby='mujian-results-panel-title'
      aria-describedby='mujian-results-panel-description'
      aria-hidden={!open}
      aria-busy={busy}
      role={modal ? 'dialog' : undefined}
      aria-modal={modal ? open || undefined : undefined}
      inert={open ? undefined : ''}
    >
      <header className='mujian-library-header'>
        <div className='mujian-library-heading'>
          <span className='mujian-library-icon' aria-hidden='true'>
            <Images size={18} />
          </span>
          <div>
            <span>当前会话</span>
            <h2 id='mujian-results-panel-title'>图片库</h2>
          </div>
        </div>
        <span className='mujian-library-count'>
          <strong>{generations.length}</strong> 个任务
        </span>
        <button
          ref={closeButtonRef}
          type='button'
          className='mujian-results-close'
          aria-label='关闭当前会话图片库'
          onClick={onClose}
        >
          <PanelRightClose size={18} aria-hidden='true' />
        </button>
      </header>
      <p
        id='mujian-results-panel-description'
        className='mujian-library-description'
      >
        只展示当前会话的图片生成任务。清空聊天不会删除已生成图片。
      </p>

      {sortedGenerations.length === 0 ? (
        <div className='mujian-library-empty'>
          <Images size={24} aria-hidden='true' />
          <strong>还没有图片任务</strong>
          <span>切换到「生成图片」，输入提示词开始创作。</span>
        </div>
      ) : (
        <div className='mujian-library-body'>
          <div className='mujian-generation-list'>
            {sortedGenerations.map((generation) => {
              const status =
                generationStatusMeta[generation.status] ||
                generationStatusMeta.queued;
              const selected =
                selectedGenerationId !== null &&
                selectedGenerationId !== '' &&
                String(generation.id) === String(selectedGenerationId);
              const detailId = `mujian-generation-${generation.id}-detail`;
              const createdDateTime = generationDateTime(generation.created_at);

              return (
                <article
                  key={generation.id}
                  className={`mujian-generation-record${selected ? ' is-selected' : ''}`}
                >
                  <button
                    type='button'
                    className='mujian-generation-summary'
                    aria-expanded={selected}
                    aria-controls={detailId}
                    onClick={() =>
                      onSelectGeneration?.(selected ? '' : generation.id)
                    }
                  >
                    <span className='mujian-generation-summary-top'>
                      <strong>
                        {generation.engine === 'gpt' ? 'GPT 生成' : 'Nano 生成'}
                      </strong>
                      <span className='mujian-generation-summary-status'>
                        <Tag color={status.color}>{status.label}</Tag>
                        <ChevronDown
                          className='mujian-generation-summary-chevron'
                          size={16}
                          aria-hidden='true'
                        />
                      </span>
                    </span>
                    <span className='mujian-generation-summary-meta'>
                      {generation.model_id || '未知模型'} ·{' '}
                      {generation.aspect_ratio || '未设置比例'}
                    </span>
                    <span className='mujian-generation-summary-prompt'>
                      {generation.prompt || '暂无提示词'}
                    </span>
                    <time
                      className='mujian-generation-summary-time'
                      dateTime={createdDateTime}
                    >
                      {formatGenerationTime(generation.created_at)}
                    </time>
                  </button>

                  <div
                    id={detailId}
                    className='mujian-generation-detail'
                    hidden={!selected}
                  >
                    {selected && (
                      <ImageGenerationCard
                        key={generation.id}
                        generation={generation}
                        sessionId={sessionId}
                        regenerating={regeneratingIds.has(generation.id)}
                        actionsDisabled={actionsDisabled}
                        onRegenerate={onRegenerate}
                      />
                    )}
                  </div>
                </article>
              );
            })}
          </div>
        </div>
      )}
    </aside>
  );
};

export default WorkspaceImageLibrary;
