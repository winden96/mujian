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

import React, { useEffect, useMemo, useState } from 'react';
import { API, showError } from '../../../helpers';
import { useTranslation } from 'react-i18next';
import { ExternalLink, FileQuestion } from 'lucide-react';
import MarkdownRenderer from '../markdown/MarkdownRenderer';
import PageState from '../ui/PageState';
import './document-renderer.css';

// Check whether content is a URL.
const isUrl = (content) => {
  try {
    const url = new URL(content.trim());
    return url.protocol === 'https:' || url.protocol === 'http:';
  } catch {
    return false;
  }
};

// Check whether content contains HTML.
const isHtmlContent = (content) => {
  if (!content || typeof content !== 'string') return false;

  const htmlTagRegex = /<\/?[a-z][\s\S]*>/i;
  return htmlTagRegex.test(content);
};

// Parse HTML content and extract inline styles.
const extractHtmlPayload = (html) => {
  const tempDiv = document.createElement('div');
  tempDiv.innerHTML = html;

  const styles = Array.from(tempDiv.querySelectorAll('style'))
    .map((style) => style.innerHTML)
    .join('\n');

  const bodyContent = tempDiv.querySelector('body');
  const content = bodyContent ? bodyContent.innerHTML : html;

  return { content, styles };
};

/**
 * 通用文档渲染组件
 * @param {string} apiEndpoint - API 接口地址
 * @param {string} title - 文档标题
 * @param {string} cacheKey - 本地存储缓存键
 * @param {string} emptyMessage - 空内容时的提示消息
 */
const DocumentRenderer = ({ apiEndpoint, title, cacheKey, emptyMessage }) => {
  const { t } = useTranslation();
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  const loadContent = async () => {
    setLoading(true);
    setLoadError(false);
    const cachedContent = localStorage.getItem(cacheKey) || '';
    if (cachedContent) {
      setContent(cachedContent);
      setLoading(false);
    }

    try {
      const res = await API.get(apiEndpoint);
      const { success, message, data } = res.data;
      if (success) {
        const nextContent = typeof data === 'string' ? data : '';
        setContent(nextContent);
        if (nextContent) {
          localStorage.setItem(cacheKey, nextContent);
        } else {
          localStorage.removeItem(cacheKey);
        }
      } else if (!cachedContent) {
        showError(message || emptyMessage);
        setContent('');
        setLoadError(true);
      }
    } catch {
      if (!cachedContent) {
        showError(emptyMessage);
        setContent('');
        setLoadError(true);
      }
    } finally {
      setLoading(false);
    }
  };

  const htmlPayload = useMemo(() => {
    return isHtmlContent(content) ? extractHtmlPayload(content) : null;
  }, [content]);
  const trimmedContent = content.trim();

  const htmlDocument = useMemo(() => {
    if (!htmlPayload) return '';
    return `<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style>
      :root { color-scheme: light; }
      * { box-sizing: border-box; }
      body { margin: 0; padding: 32px; color: #15171c; background: #fff; font: 15px/1.75 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; overflow-wrap: anywhere; }
      img, video { max-width: 100%; height: auto; }
      a { color: #2563eb; }
      ${htmlPayload.styles}
    </style>
  </head>
  <body>${htmlPayload.content}</body>
</html>`;
  }, [htmlPayload]);

  useEffect(() => {
    loadContent();
  }, []);

  // 显示加载状态
  if (loading) {
    return (
      <PageState
        busy
        eyebrow={title}
        title={t('正在加载文档')}
        description={t('正在获取管理员发布的最新内容……')}
      />
    );
  }

  // 如果没有内容，显示空状态
  if (!trimmedContent) {
    return (
      <PageState
        eyebrow={loadError ? 'CONTENT UNAVAILABLE' : 'CONTENT PENDING'}
        title={
          loadError ? t('暂时无法加载' + title) : t('管理员尚未发布' + title)
        }
        description={
          loadError
            ? t('网络连接或服务出现异常，您可以稍后重试。')
            : t('内容发布后将自动显示在这里。')
        }
        icon={<FileQuestion />}
        actions={
          loadError ? (
            <button type='button' onClick={loadContent}>
              {t('重新加载')}
            </button>
          ) : null
        }
      />
    );
  }

  // 如果是 URL，显示链接卡片
  if (isUrl(trimmedContent)) {
    return (
      <main className='mujian-document-page'>
        <section className='mujian-document-link-card'>
          <div className='mujian-document-icon' aria-hidden='true'>
            <ExternalLink />
          </div>
          <p className='mujian-document-kicker'>EXTERNAL DOCUMENT</p>
          <h1>{title}</h1>
          <p>{t('管理员将这份文档托管在外部站点。')}</p>
          <p className='mujian-document-url'>{trimmedContent}</p>
          <div>
            <a
              href={trimmedContent}
              target='_blank'
              rel='noopener noreferrer'
              title={trimmedContent}
              aria-label={`${t('访问' + title)}: ${trimmedContent}`}
              className='mujian-document-primary-action'
            >
              {t('访问' + title)}
            </a>
          </div>
        </section>
      </main>
    );
  }

  // 如果是 HTML 内容，直接渲染
  if (htmlPayload) {
    return (
      <main className='mujian-document-page'>
        <section className='mujian-document-shell'>
          <header className='mujian-document-header'>
            <p>DOCUMENT</p>
            <h1>{title}</h1>
          </header>
          <iframe
            className='mujian-document-frame'
            title={title}
            srcDoc={htmlDocument}
            sandbox=''
          />
        </section>
      </main>
    );
  }

  // 其他内容统一使用 Markdown 渲染器
  return (
    <main className='mujian-document-page'>
      <article className='mujian-document-shell'>
        <header className='mujian-document-header'>
          <p>DOCUMENT</p>
          <h1>{title}</h1>
        </header>
        <div className='mujian-document-content'>
          <div className='prose prose-lg max-w-none'>
            <MarkdownRenderer content={content} />
          </div>
        </div>
      </article>
    </main>
  );
};

export default DocumentRenderer;
