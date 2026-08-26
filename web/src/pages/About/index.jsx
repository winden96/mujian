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
import { Link } from 'react-router-dom';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import {
  ArrowRight,
  Check,
  Clapperboard,
  ExternalLink,
  FileText,
  Github,
  Image,
  Layers3,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  WandSparkles,
  Workflow,
} from 'lucide-react';
import { API, showError } from '../../helpers';
import './about.css';

const ABOUT_CACHE_KEY = 'about';

const workflowSteps = [
  {
    number: '01',
    icon: FileText,
    title: '需求解构',
    description: '把人物、场景、情绪、时长和交付目标整理成可以执行的视频任务。',
    output: '交付：视频 Brief',
  },
  {
    number: '02',
    icon: Layers3,
    title: '脚本与连续分镜',
    description: '先锁定故事节奏，再让每一镜沿着角色、场景与镜头关系继续往前。',
    output: '交付：脚本与分镜板',
  },
  {
    number: '03',
    icon: Clapperboard,
    title: '镜头生成与交付',
    description:
      '在完整项目上下文中调用 Seedance，修改、续生成与版本管理都不再断线。',
    output: '交付：可继续制作的镜头',
  },
];

const advantages = [
  {
    icon: Workflow,
    title: '上下文始终在场',
    description: '修改一句需求，不需要重新向模型介绍整个项目。',
  },
  {
    icon: WandSparkles,
    title: '步骤可见、结果可控',
    description: '需求、脚本、分镜和镜头各有明确状态，知道下一步要交付什么。',
  },
  {
    icon: ShieldCheck,
    title: '模型与制作解耦',
    description: '统一的模型路由与能力配置，让团队可以聚焦内容本身。',
  },
];

const readAboutCache = () => {
  try {
    return localStorage.getItem(ABOUT_CACHE_KEY) || '';
  } catch {
    return '';
  }
};

const writeAboutCache = (content) => {
  try {
    if (content) {
      localStorage.setItem(ABOUT_CACHE_KEY, content);
    } else {
      localStorage.removeItem(ABOUT_CACHE_KEY);
    }
  } catch {
    // Storage can be blocked by browser privacy settings; rendering still works.
  }
};

const getHttpsEmbedUrl = (content) => {
  const value = content.trim();
  if (!value) return '';

  try {
    const url = new URL(value);
    return url.protocol === 'https:' ? value : '';
  } catch {
    return '';
  }
};

const LoadNotice = ({ message, onRetry, retrying }) => (
  <div className='mujian-about-notice' role='alert'>
    <span>{message}</span>
    <button type='button' onClick={onRetry} disabled={retrying}>
      <RefreshCw size={14} aria-hidden='true' />
      {retrying ? '正在重试' : '重试'}
    </button>
  </div>
);

const AboutLoading = () => (
  <div className='mujian-about mujian-about-loading' aria-busy='true'>
    <span className='mujian-about-sr-only' role='status'>
      正在加载关于内容
    </span>
    <div className='mujian-about-loading-card' aria-hidden='true'>
      <span />
      <i />
      <i />
      <div>
        <b />
        <b />
      </div>
    </div>
  </div>
);

const DefaultAbout = ({ sourceUrl, loadError, onRetry, retrying }) => {
  const currentYear = new Date().getFullYear();

  return (
    <div className='mujian-about'>
      {loadError && (
        <div className='mujian-about-notice-wrap'>
          <LoadNotice
            message={`${loadError}，当前展示默认品牌页。`}
            onRetry={onRetry}
            retrying={retrying}
          />
        </div>
      )}

      <section className='mujian-about-hero' aria-labelledby='about-title'>
        <div className='mujian-about-halo mujian-about-halo-one' aria-hidden />
        <div className='mujian-about-halo mujian-about-halo-two' aria-hidden />
        <div className='mujian-about-hero-inner'>
          <span className='mujian-about-kicker'>
            <Sparkles size={13} aria-hidden='true' />
            从需求到成片的 AI 视频制作工作流
          </span>
          <h1 id='about-title'>
            把复杂的视频创作，
            <span>变成一条清晰制作线。</span>
          </h1>
          <p>
            幕间 AI 不只把模型放进一个对话框。它从需求出发，
            组织脚本、连续分镜与 Seedance 镜头生成，
            让每次修改都沿着同一个项目继续。
          </p>
          <div className='mujian-about-actions'>
            <Link
              className='mujian-about-primary'
              to='/console/mujian/projects'
            >
              进入项目空间 <ArrowRight size={17} aria-hidden='true' />
            </Link>
            <Link className='mujian-about-secondary' to='/pricing'>
              查看模型广场
            </Link>
          </div>
          <div className='mujian-about-hero-meta' aria-label='产品特点'>
            <span>
              <Check size={13} aria-hidden='true' /> 项目化上下文
            </span>
            <span>
              <Check size={13} aria-hidden='true' /> 连续分镜
            </span>
            <span>
              <Check size={13} aria-hidden='true' /> Seedance 镜头制作
            </span>
          </div>
        </div>
      </section>

      <section className='mujian-about-section mujian-about-workflow'>
        <div className='mujian-about-section-heading'>
          <span>ONE CLEAR PRODUCTION LINE</span>
          <h2>三个阶段，把创意稳定推进到镜头。</h2>
          <p>每一步都有清晰的输入与交付物，方便回看、调整，也方便团队协作。</p>
        </div>
        <ol className='mujian-about-step-grid'>
          {workflowSteps.map((step) => {
            const Icon = step.icon;
            return (
              <li key={step.number}>
                <div className='mujian-about-step-top'>
                  <span>{step.number}</span>
                  <Icon size={20} strokeWidth={1.7} aria-hidden='true' />
                </div>
                <h3>{step.title}</h3>
                <p>{step.description}</p>
                <small>{step.output}</small>
              </li>
            );
          })}
        </ol>
      </section>

      <section className='mujian-about-section mujian-about-advantages'>
        <div className='mujian-about-section-heading is-compact'>
          <span>BUILT FOR MAKING</span>
          <h2>不让模型打断制作。</h2>
        </div>
        <div className='mujian-about-advantage-grid'>
          {advantages.map((advantage) => {
            const Icon = advantage.icon;
            return (
              <article key={advantage.title}>
                <Icon size={21} strokeWidth={1.7} aria-hidden='true' />
                <h3>{advantage.title}</h3>
                <p>{advantage.description}</p>
              </article>
            );
          })}
        </div>
      </section>

      <section
        className='mujian-about-studio'
        aria-labelledby='about-studio-title'
      >
        <div className='mujian-about-studio-copy'>
          <span>MUJIAN STUDIO</span>
          <h2 id='about-studio-title'>
            不是一次性对话，是能继续往前的制作台。
          </h2>
          <p>
            项目空间同时保留创作对话、分镜状态、生成记录与镜头结果，
            从第一版脚本到最后一次调整都有迹可循。
          </p>
          <Link to='/console/mujian/projects'>
            打开制作台 <ArrowRight size={16} aria-hidden='true' />
          </Link>
        </div>

        <div
          className='mujian-about-studio-preview'
          aria-label='幕间 AI 制作台预览'
        >
          <div className='mujian-about-preview-bar'>
            <span>深夜便利店 · 项目工作台</span>
            <i>SYNCED</i>
          </div>
          <div className='mujian-about-preview-grid'>
            <div className='mujian-about-preview-script'>
              <span>SCRIPT · SCENE 07</span>
              <h3>雨声盖住了硬币落地的声音。</h3>
              <p>她没有回头，只在玻璃倒影里看见那盏熟悉的灯亮起来。</p>
              <div className='mujian-about-preview-status'>
                <span>
                  <Sparkles size={13} aria-hidden='true' /> 脚本已确认
                </span>
                <span>00:07 / 00:24</span>
              </div>
            </div>
            <div className='mujian-about-preview-board'>
              {['全景 · 雨夜便利店', '中景 · 林夏推门', '近景 · 硬币在手'].map(
                (shot, index) => (
                  <div key={shot}>
                    <b>0{index + 1}</b>
                    <span>{shot}</span>
                    <small>{index === 0 ? '4 秒' : '3 秒'}</small>
                  </div>
                ),
              )}
            </div>
            <figure className='mujian-about-preview-frame'>
              <img src='/cover-4.webp' alt='幕间 AI 项目中的视频分镜画面' />
              <figcaption>
                <Image size={13} aria-hidden='true' /> FRAME 03 · READY
              </figcaption>
            </figure>
          </div>
        </div>
      </section>

      <section className='mujian-about-section mujian-about-open-source'>
        <div className='mujian-about-source-mark' aria-hidden='true'>
          <Github size={28} strokeWidth={1.6} />
        </div>
        <div className='mujian-about-source-copy'>
          <span>OPEN SOURCE</span>
          <h2>开放，也是这条制作线的一部分。</h2>
          <p>
            幕间 AI 包含开源组件。使用、修改或分发时，请遵守 GNU Affero General
            Public License v3.0 的相关要求。
          </p>
          <div className='mujian-about-source-links'>
            <a href={sourceUrl} target='_blank' rel='noopener noreferrer'>
              <Github size={16} aria-hidden='true' /> 查看幕间 AI 源码
              <ExternalLink size={13} aria-hidden='true' />
            </a>
            <a
              href='https://www.gnu.org/licenses/agpl-3.0.html'
              target='_blank'
              rel='noopener noreferrer'
            >
              阅读 AGPL v3.0
              <ExternalLink size={13} aria-hidden='true' />
            </a>
          </div>
          <small>© {currentYear} 幕间 AI · 源码与开源归属信息保持可追溯</small>
        </div>
      </section>

      <section className='mujian-about-final-cta'>
        <div>
          <span>READY TO MAKE</span>
          <h2>从一句需求开始，把第一支视频推进到成片。</h2>
        </div>
        <div className='mujian-about-actions'>
          <Link className='mujian-about-primary' to='/console/mujian/projects'>
            开始第一个项目 <ArrowRight size={17} aria-hidden='true' />
          </Link>
          <Link className='mujian-about-secondary' to='/pricing'>
            先看模型与价格
          </Link>
        </div>
      </section>
    </div>
  );
};

const CustomAbout = ({ content, loadError, onRetry, retrying }) => {
  const embedUrl = getHttpsEmbedUrl(content);
  const html = useMemo(
    () =>
      embedUrl
        ? ''
        : DOMPurify.sanitize(marked.parse(content), {
            USE_PROFILES: { html: true },
          }),
    [content, embedUrl],
  );
  const [iframeReady, setIframeReady] = useState(false);
  const [iframeRevision, setIframeRevision] = useState(0);

  useEffect(() => {
    setIframeReady(false);
  }, [embedUrl, iframeRevision]);

  if (embedUrl) {
    return (
      <div className='mujian-about-custom-shell'>
        {loadError && (
          <LoadNotice
            message={`${loadError}，已保留上次成功加载的内容。`}
            onRetry={onRetry}
            retrying={retrying}
          />
        )}
        <div className='mujian-about-iframe-shell'>
          {!iframeReady && (
            <div className='mujian-about-iframe-loading' role='status'>
              <RefreshCw size={18} aria-hidden='true' />
              <span>正在加载管理员设置的关于页…</span>
              <button
                type='button'
                onClick={() => setIframeRevision((value) => value + 1)}
              >
                重新加载
              </button>
            </div>
          )}
          <iframe
            key={`${embedUrl}-${iframeRevision}`}
            src={embedUrl}
            title='幕间 AI 自定义关于内容'
            onLoad={() => setIframeReady(true)}
            referrerPolicy='strict-origin-when-cross-origin'
            sandbox='allow-forms allow-popups allow-popups-to-escape-sandbox allow-scripts'
            allowFullScreen
          />
        </div>
        <a
          className='mujian-about-iframe-external'
          href={embedUrl}
          target='_blank'
          rel='noopener noreferrer'
        >
          在新窗口打开 <ExternalLink size={13} aria-hidden='true' />
        </a>
      </div>
    );
  }

  return (
    <div className='mujian-about-custom-shell'>
      {loadError && (
        <LoadNotice
          message={`${loadError}，已保留上次成功加载的内容。`}
          onRetry={onRetry}
          retrying={retrying}
        />
      )}
      <article
        className='mujian-about-custom-document'
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </div>
  );
};

const About = () => {
  const initialContent = useMemo(readAboutCache, []);
  const [about, setAbout] = useState(initialContent);
  const [loading, setLoading] = useState(!initialContent);
  const [retrying, setRetrying] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [requestRevision, setRequestRevision] = useState(0);
  const sourceUrl =
    import.meta.env.VITE_AGPL_SOURCE_URL ||
    'https://github.com/winden96/mujian';

  useEffect(() => {
    document.body.classList.add('mujian-about-route');
    return () => document.body.classList.remove('mujian-about-route');
  }, []);

  useEffect(() => {
    let active = true;
    const cachedContent = readAboutCache();

    if (cachedContent) {
      setAbout(cachedContent);
      setLoading(false);
    } else if (requestRevision === 0) {
      setLoading(true);
    }

    const loadAbout = async () => {
      try {
        const response = await API.get('/api/about');
        const { success, message, data } = response.data || {};

        if (!success) {
          throw new Error(message || '加载关于内容失败');
        }

        const nextContent = typeof data === 'string' ? data : '';
        if (!active) return;

        const resolvedContent = nextContent.trim() ? nextContent : '';
        setLoadError('');
        setAbout(resolvedContent);
        writeAboutCache(resolvedContent);
      } catch (error) {
        if (!active) return;

        const message =
          error?.response?.data?.message ||
          error?.message ||
          '加载关于内容失败';
        setLoadError(message);
        if (!cachedContent) showError(message);
      } finally {
        if (active) {
          setLoading(false);
          setRetrying(false);
        }
      }
    };

    loadAbout();
    return () => {
      active = false;
    };
  }, [requestRevision]);

  const retry = () => {
    setRetrying(true);
    setRequestRevision((value) => value + 1);
  };

  if (loading) return <AboutLoading />;

  if (about.trim()) {
    return (
      <CustomAbout
        content={about}
        loadError={loadError}
        onRetry={retry}
        retrying={retrying}
      />
    );
  }

  return (
    <DefaultAbout
      sourceUrl={sourceUrl}
      loadError={loadError}
      onRetry={retry}
      retrying={retrying}
    />
  );
};

export default About;
