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

import React, { useCallback, useEffect, useId, useRef, useState } from 'react';
import { Button, Dropdown, Select, TextArea } from '@douyinfe/semi-ui';
import {
  Check,
  ChevronDown,
  FileText,
  Image as ImageIcon,
  LoaderCircle,
  MessageCircle,
  MessagesSquare,
  MoreHorizontal,
  Paperclip,
  PanelRightClose,
  PanelRightOpen,
  Plus,
  Send,
  Settings2,
  Sparkles,
  Trash2,
  Upload,
  X,
} from 'lucide-react';
import { agentAttachmentAccept, formatAttachmentSize } from './attachments';
import { imageReferenceAccept } from './imageGenerations';

const SELECT_ARROW = (
  <ChevronDown size={14} aria-hidden='true' focusable='false' />
);

const starterPrompts = {
  chat: ['写一个开场钩子', '梳理故事节奏', '优化人物对白'],
  image: ['雨夜霓虹街头', '电影感人物近景', '东方奇幻大场景'],
};

const optionList = (options = []) =>
  options.map((option) =>
    typeof option === 'string' ? { label: option, value: option } : option,
  );

const imageAspectRatioOptions = [
  { label: '9:16 竖屏', value: '9:16' },
  { label: '16:9 横屏', value: '16:9' },
  { label: '1:1 方形', value: '1:1' },
];

const sessionChangeLabels = {
  switching: '正在切换会话…',
  creating: '正在创建会话…',
  clearing: '正在清空会话…',
};

const SessionControls = ({
  sessions,
  activeSessionId,
  changing,
  busy,
  canClear,
  onChange,
  onNew,
  onClear,
}) => {
  const sessionLabelId = useId();
  const sessionOptions = sessions.map((session) => ({
    label: session.title || '新会话',
    value: session.id,
  }));
  const clearMenuItem = {
    node: 'item',
    key: 'clear-session',
    name: '清空当前会话',
    type: 'danger',
    disabled: busy || !canClear,
    onClick: onClear,
    children: (
      <span className='mujian-session-menu-item is-danger'>
        <Trash2 size={14} aria-hidden='true' />
        <span>清空当前会话</span>
      </span>
    ),
  };
  const mobileMenu = [
    ...sessions.map((session) => ({
      node: 'item',
      key: session.id,
      name: session.title || '新会话',
      disabled: busy || session.id === activeSessionId,
      onClick: () => onChange(session.id),
      children: (
        <span className='mujian-session-menu-item' title={session.title}>
          <MessageCircle size={14} aria-hidden='true' />
          <span>{session.title || '新会话'}</span>
          {session.id === activeSessionId && (
            <Check size={14} aria-label='当前会话' />
          )}
        </span>
      ),
    })),
    ...(sessions.length > 0
      ? [{ node: 'divider', key: 'session-divider' }]
      : []),
    {
      node: 'item',
      key: 'new-session',
      name: '新建会话',
      disabled: busy,
      onClick: onNew,
      children: (
        <span className='mujian-session-menu-item'>
          <Plus size={14} aria-hidden='true' />
          <span>新建会话</span>
        </span>
      ),
    },
    clearMenuItem,
  ];

  return (
    <>
      <div className='mujian-session-desktop-actions'>
        <span id={sessionLabelId} className='mujian-visually-hidden'>
          当前会话
        </span>
        <Select
          className='mujian-session-select'
          aria-labelledby={sessionLabelId}
          defaultActiveFirstOption={false}
          arrowIcon={SELECT_ARROW}
          value={activeSessionId || undefined}
          placeholder='选择会话'
          optionList={sessionOptions}
          maxHeight={320}
          disabled={busy || sessions.length === 0}
          loading={Boolean(changing)}
          onChange={onChange}
        />
        <Button
          className='mujian-session-new'
          theme='borderless'
          type='tertiary'
          icon={<Plus size={15} aria-hidden='true' />}
          disabled={busy}
          loading={changing === 'creating'}
          onClick={onNew}
        >
          新会话
        </Button>
        <Dropdown trigger='click' position='bottomRight' menu={[clearMenuItem]}>
          <Button
            className='mujian-session-more'
            aria-label='更多会话操作'
            title='更多会话操作'
            theme='borderless'
            type='tertiary'
            icon={<MoreHorizontal size={17} aria-hidden='true' />}
            disabled={busy}
          />
        </Dropdown>
      </div>

      <div className='mujian-session-mobile-actions'>
        <Dropdown
          trigger='click'
          position='bottomRight'
          contentClassName='mujian-session-mobile-dropdown'
          menu={mobileMenu}
        >
          <Button
            className='mujian-session-mobile-trigger'
            aria-label={`会话菜单，当前为${
              sessions.find((session) => session.id === activeSessionId)
                ?.title || '新会话'
            }`}
            title='会话菜单'
            theme='borderless'
            type='tertiary'
            icon={<MessagesSquare size={17} aria-hidden='true' />}
            disabled={busy}
            loading={Boolean(changing)}
          />
        </Dropdown>
      </div>
    </>
  );
};

const ReasoningDisclosure = ({ message }) => {
  const hasContent = Boolean(message.content);
  const [open, setOpen] = useState(Boolean(message.streaming && !hasContent));
  const sawContent = useRef(hasContent);
  const wasStreaming = useRef(Boolean(message.streaming));

  useEffect(() => {
    if (!sawContent.current && hasContent) {
      sawContent.current = true;
      setOpen(false);
    }
    if (wasStreaming.current && !message.streaming) {
      setOpen(false);
    }
    wasStreaming.current = Boolean(message.streaming);
  }, [hasContent, message.streaming]);

  const reasoningText =
    message.reasoning ||
    (message.streaming
      ? '正在等待模型返回思考过程…'
      : '本次渠道未提供思考过程');

  return (
    <details
      className='mujian-agent-reasoning'
      open={open}
      aria-live='off'
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        <span>
          <Sparkles size={13} aria-hidden='true' />
          思考过程
        </span>
        {message.streaming && !hasContent && (
          <span className='mujian-agent-reasoning-status'>
            <LoaderCircle size={12} aria-hidden='true' />
            思考中
          </span>
        )}
        <ChevronDown
          className='mujian-agent-reasoning-chevron'
          size={14}
          aria-hidden='true'
        />
      </summary>
      <div
        className={`mujian-agent-reasoning-content${message.reasoning ? '' : ' is-empty'}`}
      >
        {reasoningText}
      </div>
    </details>
  );
};

const MessageEntry = ({ message, currentSkill, currentModel }) => {
  const isUser = message.role === 'user';
  const messageModel =
    message.model_id || (message.streaming ? currentModel : '');
  const hasReasoning = !isUser && typeof message.reasoning === 'string';

  return (
    <article
      className={`mujian-agent-message ${isUser ? 'user' : 'assistant'}`}
      aria-label={isUser ? '你发送的消息' : '创作 Agent 的回复'}
    >
      {!isUser && (
        <header className='mujian-agent-message-meta'>
          <span>
            <Sparkles size={13} aria-hidden='true' />
            {message.skill || currentSkill}
          </span>
          {messageModel && <span>{messageModel}</span>}
          {message.streaming && (
            <span className='mujian-agent-streaming' role='status'>
              <LoaderCircle size={12} aria-hidden='true' />
              回复中
            </span>
          )}
        </header>
      )}
      <div className='mujian-agent-message-bubble'>
        <span className='mujian-visually-hidden'>
          {isUser ? '你：' : '创作 Agent：'}
        </span>
        {hasReasoning && <ReasoningDisclosure message={message} />}
        {message.content && <p>{message.content}</p>}
      </div>
    </article>
  );
};

const EmptyThread = ({ creationMode, onChooseStarter }) => (
  <div className='mujian-agent-empty'>
    <span aria-hidden='true'>
      <Sparkles size={18} />
    </span>
    <strong>
      {creationMode === 'chat' ? '从一个念头开始' : '生成第一个画面'}
    </strong>
    <p>
      {creationMode === 'chat'
        ? '描述故事或上传资料，Agent 会和你一起推进。'
        : '说清主体、场景、光线和构图。'}
    </p>
    <div>
      {starterPrompts[creationMode].map((prompt) => (
        <button
          key={prompt}
          type='button'
          onClick={() => onChooseStarter(prompt)}
        >
          {prompt}
        </button>
      ))}
    </div>
  </div>
);

const ChatComposer = ({ chat, busy, forceNextScroll }) => {
  const attachmentInput = useRef(null);
  const canCompose = chat.modelAvailable && chat.skillEnabled;
  const submitDisabled =
    !canCompose ||
    (!chat.value.trim() && chat.attachments.length === 0) ||
    busy ||
    chat.readingAttachments ||
    chat.savingModel;

  const submit = (event) => {
    event.preventDefault();
    if (submitDisabled) return;
    forceNextScroll.current = true;
    chat.onSubmit();
  };

  const handleKeyDown = (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      submit(event);
    }
  };

  const skills = optionList(chat.skillOptions).map((skill) => ({
    ...skill,
    disabled: skill.disabled || !chat.enabledSkills.includes(skill.label),
  }));

  return (
    <form
      className='mujian-agent-composer is-chat'
      aria-label='对话输入'
      onSubmit={submit}
    >
      <input
        ref={attachmentInput}
        className='mujian-agent-file-input'
        type='file'
        accept={agentAttachmentAccept}
        multiple
        disabled={busy || chat.readingAttachments}
        onChange={chat.onAttachmentsChange}
      />

      {chat.attachments.length > 0 && (
        <div className='mujian-agent-attachment-list'>
          {chat.attachments.map((attachment) => (
            <div className='mujian-agent-attachment' key={attachment.id}>
              {attachment.preview_url ? (
                <img src={attachment.preview_url} alt='' />
              ) : (
                <FileText size={19} aria-hidden='true' />
              )}
              <span>
                <strong title={attachment.name}>{attachment.name}</strong>
                <small>{formatAttachmentSize(attachment.size)}</small>
              </span>
              <button
                type='button'
                aria-label={`移除附件 ${attachment.name}`}
                disabled={busy || chat.readingAttachments}
                onClick={() => chat.onRemoveAttachment(attachment.id)}
              >
                <X size={14} aria-hidden='true' />
              </button>
            </div>
          ))}
        </div>
      )}

      <TextArea
        className='mujian-agent-input'
        aria-label='给创作 Agent 的指令'
        value={chat.value}
        onChange={chat.onValueChange}
        onKeyDown={handleKeyDown}
        autosize={{ minRows: 2, maxRows: 7 }}
        borderless
        disabled={!canCompose || busy}
        placeholder={
          canCompose
            ? '输入灵感，或告诉 Agent 下一步怎么做…'
            : chat.skillEnabled
              ? '配置可用对话模型后即可开始创作'
              : `启用「${chat.skill || '当前 Skill'}」后即可开始创作`
        }
      />

      {!chat.modelAvailable && (
        <div className='mujian-model-unavailable' role='status'>
          {chat.hasModels
            ? '请在高级设置中选择可用的对话模型。'
            : '当前没有可用的对话模型，请联系管理员配置。'}
        </div>
      )}
      {chat.modelAvailable && !chat.skillEnabled && (
        <div className='mujian-model-unavailable' role='status'>
          当前 Skill 未启用，请先在 Skills 页面启用。
        </div>
      )}

      <details className='mujian-agent-advanced'>
        <summary>
          <Settings2 size={14} aria-hidden='true' />
          高级设置
          <ChevronDown size={14} aria-hidden='true' />
        </summary>
        <div className='mujian-agent-fields'>
          <label>
            <span>Skill</span>
            <Select
              defaultActiveFirstOption={false}
              arrowIcon={SELECT_ARROW}
              value={chat.skill}
              disabled={busy}
              onChange={chat.onSkillChange}
              optionList={skills}
            />
          </label>
          <label>
            <span>对话模型</span>
            <Select
              defaultActiveFirstOption={false}
              arrowIcon={SELECT_ARROW}
              value={chat.modelAvailable ? chat.modelId : undefined}
              placeholder={chat.hasModels ? '选择对话模型' : '暂无可用模型'}
              disabled={!chat.hasModels || busy || chat.savingModel}
              loading={chat.savingModel}
              onChange={chat.onModelChange}
              optionList={optionList(chat.modelOptions)}
            />
          </label>
        </div>
      </details>

      <footer className='mujian-agent-actions'>
        <Button
          aria-label='添加附件'
          title='添加附件'
          theme='borderless'
          type='tertiary'
          icon={<Paperclip size={16} aria-hidden='true' />}
          disabled={!canCompose || busy || chat.readingAttachments}
          onClick={() => attachmentInput.current?.click()}
        >
          附件
        </Button>
        <Button
          className='mujian-agent-submit'
          aria-label='发送消息'
          theme='solid'
          htmlType='submit'
          icon={<Send size={16} aria-hidden='true' />}
          disabled={submitDisabled}
          loading={busy}
        >
          发送
        </Button>
      </footer>
    </form>
  );
};

const ImageComposer = ({ image }) => {
  const referenceInput = useRef(null);
  const modelLabelId = useId();
  const aspectRatioLabelId = useId();
  const submitDisabled =
    !image.value.trim() ||
    !image.selectedModelAvailable ||
    image.submitting ||
    image.interactionDisabled ||
    image.savingModel;

  const submit = (event) => {
    event.preventDefault();
    if (submitDisabled) return;
    image.onSubmit();
  };

  const handleKeyDown = (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
      submit(event);
    }
  };

  return (
    <form
      className='mujian-agent-composer is-image'
      aria-label='图片生成输入'
      onSubmit={submit}
    >
      <input
        ref={referenceInput}
        className='mujian-agent-file-input'
        type='file'
        accept={imageReferenceAccept}
        multiple
        disabled={
          image.submitting ||
          image.interactionDisabled ||
          image.references.length >= image.maxReferences
        }
        onChange={image.onReferencesChange}
      />

      <div
        className='mujian-agent-engine-switch'
        role='group'
        aria-label='图片生成引擎'
      >
        {optionList(image.engineOptions).map((engine) => (
          <button
            key={engine.value}
            type='button'
            aria-pressed={image.engine === engine.value}
            className={image.engine === engine.value ? 'is-active' : ''}
            disabled={
              engine.disabled || image.submitting || image.interactionDisabled
            }
            onClick={() => image.onEngineChange(engine.value)}
          >
            {engine.label}
          </button>
        ))}
        <span>0–{image.maxReferences} 张参考图</span>
      </div>

      <TextArea
        className='mujian-agent-input'
        aria-label='图片生成提示词'
        value={image.value}
        onChange={image.onValueChange}
        onKeyDown={handleKeyDown}
        autosize={{ minRows: 2, maxRows: 7 }}
        borderless
        disabled={image.submitting || image.interactionDisabled}
        maxLength={8000}
        placeholder='描述主体、场景、光线和构图…'
      />

      {image.references.length > 0 && (
        <div className='mujian-agent-reference-list'>
          {image.references.map((reference, index) => (
            <figure key={reference.id}>
              <img
                src={reference.previewUrl}
                alt={`参考图 ${index + 1}：${reference.name}`}
              />
              <figcaption>{index + 1}</figcaption>
              <button
                type='button'
                aria-label={`移除参考图 ${reference.name}`}
                disabled={image.submitting || image.interactionDisabled}
                onClick={() => image.onRemoveReference(reference.id)}
              >
                <X size={13} aria-hidden='true' />
              </button>
            </figure>
          ))}
        </div>
      )}

      <div className='mujian-agent-actions is-image'>
        <Button
          className='mujian-agent-reference-trigger'
          aria-label='添加参考图'
          title='添加参考图'
          theme='borderless'
          type='tertiary'
          icon={<Upload size={16} aria-hidden='true' />}
          disabled={
            image.submitting ||
            image.interactionDisabled ||
            image.references.length >= image.maxReferences
          }
          onClick={() => referenceInput.current?.click()}
        >
          参考图 {image.references.length}/{image.maxReferences}
        </Button>
        <div className='mujian-agent-inline-model'>
          <span id={modelLabelId} className='mujian-visually-hidden'>
            具体模型
          </span>
          <Select
            aria-labelledby={modelLabelId}
            defaultActiveFirstOption={false}
            arrowIcon={SELECT_ARROW}
            value={image.selectedModel || undefined}
            placeholder='暂无可用模型'
            dropdownClassName='mujian-image-model-dropdown'
            disabled={
              !image.hasFamilyModels ||
              image.submitting ||
              image.savingModel ||
              image.interactionDisabled
            }
            loading={image.savingModel}
            onChange={image.onModelChange}
            optionList={optionList(image.modelOptions)}
          />
        </div>
        <div className='mujian-agent-inline-aspect-ratio'>
          <span id={aspectRatioLabelId} className='mujian-visually-hidden'>
            图片比例
          </span>
          <Select
            aria-labelledby={aspectRatioLabelId}
            defaultActiveFirstOption={false}
            arrowIcon={SELECT_ARROW}
            value={image.aspectRatio}
            dropdownClassName='mujian-image-aspect-ratio-dropdown'
            disabled={image.submitting || image.interactionDisabled}
            onChange={image.onAspectRatioChange}
            optionList={imageAspectRatioOptions}
          />
        </div>
        <Button
          className='mujian-agent-submit'
          aria-label='生成图片'
          theme='solid'
          htmlType='submit'
          icon={<ImageIcon size={16} aria-hidden='true' />}
          disabled={submitDisabled}
          loading={image.submitting}
        >
          生成
        </Button>
      </div>

      {!image.hasFamilyModels && (
        <div className='mujian-model-unavailable' role='status'>
          当前没有可用的 {image.engine === 'gpt' ? 'GPT' : 'Nano'} 生图模型。
        </div>
      )}
      {image.references.length > 0 &&
        image.referenceUnavailableReason &&
        !image.selectedModelAvailable && (
          <div className='mujian-model-unavailable' role='status'>
            {image.referenceUnavailableReason}
          </div>
        )}
    </form>
  );
};

const WorkspaceAgentPanel = ({
  creationMode,
  messages,
  projectTitle,
  episode,
  resultsOpen,
  resultCount,
  resultsBusy,
  resultsToggleRef,
  currentSkill,
  currentModel,
  sending,
  sessions,
  activeSessionId,
  sessionChanging,
  sessionControlsBusy,
  canClearSession,
  onCreationModeChange,
  onToggleResults,
  onSessionChange,
  onNewSession,
  onClearSession,
  chat,
  image,
}) => {
  const threadRef = useRef(null);
  const nearBottom = useRef(true);
  const forceNextScroll = useRef(false);

  const updateScrollPosition = useCallback(() => {
    const thread = threadRef.current;
    if (!thread) return;
    nearBottom.current =
      thread.scrollHeight - thread.scrollTop - thread.clientHeight <= 80;
  }, []);

  useEffect(() => {
    if (!nearBottom.current && !forceNextScroll.current) return undefined;

    const frame = window.requestAnimationFrame(() => {
      const thread = threadRef.current;
      if (thread) {
        thread.scrollTop = thread.scrollHeight;
        nearBottom.current = true;
      }
      forceNextScroll.current = false;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [messages]);

  useEffect(() => {
    if (!activeSessionId || sessionChanging) return undefined;
    nearBottom.current = true;
    forceNextScroll.current = true;
    const frame = window.requestAnimationFrame(() => {
      const thread = threadRef.current;
      if (thread) thread.scrollTop = thread.scrollHeight;
      forceNextScroll.current = false;
    });
    return () => window.cancelAnimationFrame(frame);
  }, [activeSessionId, sessionChanging]);

  const activeStarter =
    creationMode === 'chat' ? chat.onChooseStarter : image.onChooseStarter;
  const operationBusy = sending || image.submitting || Boolean(sessionChanging);
  const panelBusy = operationBusy;
  const resultsStatus = resultsBusy ? '更新中' : `${resultCount} 项`;

  return (
    <section
      className='mujian-agent-panel'
      aria-label='创作 Agent'
      aria-busy={panelBusy || undefined}
    >
      <header className='mujian-agent-titlebar'>
        <span className='mujian-agent-brand' aria-hidden='true'>
          <Sparkles size={17} />
        </span>
        <div className='mujian-agent-heading'>
          <div className='mujian-agent-identity'>
            <strong>创作 Agent</strong>
            <small>{currentModel || '等待选择模型'}</small>
          </div>
          <div className='mujian-agent-context'>
            <span>第 {episode || 1} 集</span>
            <strong title={projectTitle}>{projectTitle || '创作工作区'}</strong>
          </div>
        </div>
        <div className='mujian-agent-title-actions'>
          <SessionControls
            sessions={sessions}
            activeSessionId={activeSessionId}
            changing={sessionChanging}
            busy={sessionControlsBusy}
            canClear={canClearSession}
            onChange={onSessionChange}
            onNew={onNewSession}
            onClear={onClearSession}
          />
          <span ref={resultsToggleRef} className='mujian-results-toggle-anchor'>
            <Button
              className='mujian-results-toggle'
              aria-label={`${resultsOpen ? '收起' : '打开'}当前会话图片库，${resultsBusy ? '正在更新' : `共 ${resultCount} 项`}`}
              aria-controls='mujian-results-panel'
              aria-expanded={resultsOpen}
              title={`${resultsOpen ? '收起' : '打开'}当前会话图片库`}
              theme='borderless'
              type='tertiary'
              icon={
                resultsOpen ? (
                  <PanelRightClose size={17} aria-hidden='true' />
                ) : (
                  <PanelRightOpen size={17} aria-hidden='true' />
                )
              }
              onClick={onToggleResults}
            >
              <span className='mujian-results-toggle-status'>
                {resultsBusy && (
                  <LoaderCircle
                    className='mujian-spin'
                    size={13}
                    aria-hidden='true'
                  />
                )}
                <span>{resultsStatus}</span>
              </span>
            </Button>
          </span>
        </div>
      </header>

      <div
        className='mujian-agent-mode-switch'
        role='group'
        aria-label='创作模式'
      >
        <button
          type='button'
          aria-pressed={creationMode === 'chat'}
          className={creationMode === 'chat' ? 'is-active' : ''}
          disabled={operationBusy}
          onClick={() => onCreationModeChange('chat')}
        >
          <MessageCircle size={15} aria-hidden='true' />
          对话
        </button>
        <button
          type='button'
          aria-pressed={creationMode === 'image'}
          className={creationMode === 'image' ? 'is-active' : ''}
          disabled={operationBusy}
          onClick={() => onCreationModeChange('image')}
        >
          <ImageIcon size={15} aria-hidden='true' />
          生成图片
        </button>
      </div>

      <div
        ref={threadRef}
        className='mujian-agent-thread'
        role='log'
        aria-label='创作记录'
        aria-live='polite'
        aria-relevant='additions text'
        aria-busy={panelBusy || undefined}
        onScroll={updateScrollPosition}
      >
        {sessionChanging ? (
          <div className='mujian-session-loading' role='status'>
            <LoaderCircle size={18} aria-hidden='true' />
            <span>
              {sessionChangeLabels[sessionChanging] || '正在加载会话…'}
            </span>
          </div>
        ) : messages.length === 0 ? (
          <EmptyThread
            creationMode={creationMode}
            onChooseStarter={activeStarter}
          />
        ) : (
          messages.map((message) => (
            <MessageEntry
              key={message.id}
              message={message}
              currentSkill={currentSkill}
              currentModel={currentModel}
            />
          ))
        )}
      </div>

      {creationMode === 'chat' ? (
        <ChatComposer
          chat={chat}
          busy={operationBusy}
          forceNextScroll={forceNextScroll}
        />
      ) : (
        <ImageComposer image={image} />
      )}
    </section>
  );
};

export default WorkspaceAgentPanel;
