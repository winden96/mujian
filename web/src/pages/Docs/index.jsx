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

import React, {
  lazy,
  Suspense,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
import { Link } from 'react-router-dom';
import {
  ArrowRight,
  BookOpen,
  Braces,
  Check,
  CircleDollarSign,
  Copy,
  Download,
  KeyRound,
  ShieldAlert,
  Terminal,
} from 'lucide-react';
import { StatusContext } from '../../context/Status';
import { UserContext } from '../../context/User';
import { copy, resolveApiBaseURL, resolveGatewayAddress } from '../../helpers';
import {
  MUJIAN_TOKEN_PATH,
  withAuthReturnTarget,
} from '../../helpers/authReturn';
import { createQuickstarts, withRuntimeServer } from './docsRuntime';
import '../mujian.css';

const SwaggerReference = lazy(() => import('./SwaggerReference'));

const API_GROUPS = [
  '模型',
  'Chat · Responses · Claude',
  '图像 · 音频',
  'Embedding · Rerank',
  'Gemini · Realtime',
  '视频 · Midjourney · Suno',
];

const ONBOARDING_STEPS = [
  {
    number: '01',
    title: '获取个人 Token',
    description:
      '登录后在 API Key 页自己创建密钥，用它调用幕间的接口。',
    actionLabel: '生成 Token',
  },
  {
    number: '02',
    title: '选择兼容协议',
    description:
      '优先使用 OpenAI 兼容的 /v1 地址；Claude、Gemini 等原生路径见下方参考。',
  },
  {
    number: '03',
    title: '先查模型，再发请求',
    description:
      '调用 GET /v1/models 确认当前可用模型，再用同一 Token 调用目标接口。',
  },
];

const Docs = () => {
  const [statusState] = useContext(StatusContext);
  const [userState] = useContext(UserContext);
  const [spec, setSpec] = useState(null);
  const [specError, setSpecError] = useState('');
  const [reloadKey, setReloadKey] = useState(0);
  const [activeSnippet, setActiveSnippet] = useState('curl');
  const [copiedValue, setCopiedValue] = useState('');

  const gatewayAddress = resolveGatewayAddress(
    statusState?.status?.server_address,
    window.location.origin,
  );
  const apiBaseURL = resolveApiBaseURL(
    statusState?.status?.server_address,
    window.location.origin,
  );
  const quickstarts = useMemo(
    () => createQuickstarts(apiBaseURL),
    [apiBaseURL],
  );
  const selectedSnippet =
    quickstarts.find((snippet) => snippet.id === activeSnippet) ||
    quickstarts[0];
  const runtimeSpec = useMemo(
    () => (spec ? withRuntimeServer(spec, gatewayAddress) : null),
    [gatewayAddress, spec],
  );
  const tokenHref = userState?.user
    ? MUJIAN_TOKEN_PATH
    : withAuthReturnTarget('/login', MUJIAN_TOKEN_PATH);

  useEffect(() => {
    const controller = new AbortController();
    setSpec(null);
    setSpecError('');

    fetch('/api/openapi/relay.json', {
      credentials: 'omit',
      headers: { Accept: 'application/json' },
      signal: controller.signal,
    })
      .then((response) => {
        if (!response.ok) {
          throw new Error(`HTTP ${response.status}`);
        }
        return response.json();
      })
      .then(setSpec)
      .catch((error) => {
        if (error.name !== 'AbortError') {
          setSpecError(`OpenAPI 文档加载失败（${error.message}）`);
        }
      });

    return () => controller.abort();
  }, [reloadKey]);

  const copyText = async (value, key) => {
    if (await copy(value)) setCopiedValue(key);
  };

  const downloadSpec = () => {
    if (!runtimeSpec) return;
    const objectURL = URL.createObjectURL(
      new Blob([JSON.stringify(runtimeSpec, null, 2)], {
        type: 'application/json',
      }),
    );
    const anchor = document.createElement('a');
    anchor.href = objectURL;
    anchor.download = 'mujian-openapi.json';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    window.setTimeout(() => URL.revokeObjectURL(objectURL), 0);
  };

  return (
    <main className='mujian-docs-page'>
      <section className='mujian-docs-hero' aria-labelledby='docs-title'>
        <div className='mujian-docs-hero-copy'>
          <p className='mujian-docs-kicker'>API · 幕间接入</p>
          <h1 id='docs-title'>
            用一套稳定接口，
            <em>调用幕间的多模型能力。</em>
          </h1>
          <p>
            从第一个对话请求到图像、音频与视频工作流，在当前站点完成接入、查阅与在线调试。
          </p>
          <div className='mujian-docs-hero-actions'>
            <a className='mujian-docs-primary-link' href='#api-reference'>
              查看完整接口 <ArrowRight size={16} />
            </a>
            <Link className='mujian-docs-secondary-link' to={tokenHref}>
              生成 API Key
            </Link>
          </div>
        </div>

        <aside className='mujian-docs-endpoint-card' aria-label='当前接入地址'>
          <div className='mujian-docs-endpoint-heading'>
            <span>当前 API Base URL</span>
            <span className='mujian-docs-live-badge'>
              <i aria-hidden='true' /> LIVE
            </span>
          </div>
          <div className='mujian-docs-endpoint-value'>
            <code>{apiBaseURL}</code>
            <button
              type='button'
              aria-label='复制 API Base URL'
              onClick={() => copyText(apiBaseURL, 'base-url')}
            >
              {copiedValue === 'base-url' ? (
                <Check size={16} />
              ) : (
                <Copy size={16} />
              )}
            </button>
          </div>
          <dl>
            <div>
              <dt>认证</dt>
              <dd>Bearer Token</dd>
            </div>
            <div>
              <dt>格式</dt>
              <dd>JSON · SSE · WebSocket</dd>
            </div>
          </dl>
          <p>地址来自当前服务配置；未配置时自动使用本站同源地址。</p>
        </aside>
      </section>

      <div className='mujian-docs-content'>
        <section className='mujian-docs-capabilities' aria-label='可用能力'>
          {API_GROUPS.map((group) => (
            <span key={group}>{group}</span>
          ))}
        </section>

        <section className='mujian-docs-section' aria-labelledby='start-title'>
          <div className='mujian-docs-section-heading'>
            <span>01 · GET STARTED</span>
            <h2 id='start-title'>三步完成首次调用</h2>
            <p>不需要了解内部渠道，只需 Base URL、Token 和模型名。</p>
          </div>
          <ol className='mujian-docs-steps'>
            {ONBOARDING_STEPS.map((step) => (
              <li key={step.number}>
                <span>{step.number}</span>
                <div>
                  <h3>{step.title}</h3>
                  <p>{step.description}</p>
                  {step.actionLabel ? (
                    <Link className='mujian-docs-step-action' to={tokenHref}>
                      {step.actionLabel} <ArrowRight size={14} />
                    </Link>
                  ) : null}
                </div>
              </li>
            ))}
          </ol>
        </section>

        <section className='mujian-docs-section' aria-labelledby='quick-title'>
          <div className='mujian-docs-section-heading'>
            <span>02 · QUICKSTART</span>
            <h2 id='quick-title'>复制即用的最小示例</h2>
            <p>示例中的模型名仅作占位，请以 GET /v1/models 的实时返回为准。</p>
          </div>
          <div className='mujian-docs-code-panel'>
            <div className='mujian-docs-code-toolbar'>
              <div className='mujian-docs-code-languages' aria-label='代码语言'>
                {quickstarts.map((snippet) => (
                  <button
                    key={snippet.id}
                    type='button'
                    aria-pressed={activeSnippet === snippet.id}
                    className={activeSnippet === snippet.id ? 'is-active' : ''}
                    onClick={() => setActiveSnippet(snippet.id)}
                  >
                    {snippet.label}
                  </button>
                ))}
              </div>
              <button
                type='button'
                className='mujian-docs-copy-code'
                onClick={() =>
                  copyText(
                    selectedSnippet.code,
                    `snippet-${selectedSnippet.id}`,
                  )
                }
              >
                {copiedValue === `snippet-${selectedSnippet.id}` ? (
                  <Check size={14} />
                ) : (
                  <Copy size={14} />
                )}
                {copiedValue === `snippet-${selectedSnippet.id}`
                  ? '已复制'
                  : '复制'}
              </button>
            </div>
            <pre>
              <code className={`language-${selectedSnippet.language}`}>
                {selectedSnippet.code}
              </code>
            </pre>
          </div>
        </section>

        <section className='mujian-docs-notices' aria-label='调试提醒'>
          <article>
            <KeyRound size={20} />
            <div>
              <h3>Token 只留在当前页面</h3>
              <p>
                点击下方 Authorize 后手动粘贴 Bearer Token。页面不会读取个人
                Token，也不持久化授权，刷新后即清空。
              </p>
            </div>
          </article>
          <article>
            <CircleDollarSign size={20} />
            <div>
              <h3>在线调试会发送真实请求</h3>
              <p>
                Execute
                会直接调用网关与上游模型，成功请求可能消耗额度。请优先使用低成本模型和短输出。
              </p>
            </div>
          </article>
          <article>
            <ShieldAlert size={20} />
            <div>
              <h3>先核对错误，再重试</h3>
              <p>
                401 通常表示 Token 无效，403 表示模型或权限受限，429
                表示额度或频率超限。请避免无上限自动重试。
              </p>
            </div>
          </article>
        </section>

        <section
          id='api-reference'
          className='mujian-docs-section mujian-docs-reference-section'
          aria-labelledby='reference-title'
        >
          <div className='mujian-docs-reference-heading'>
            <div className='mujian-docs-section-heading'>
              <span>03 · API REFERENCE</span>
              <h2 id='reference-title'>完整接口参考</h2>
              <p>支持搜索、深链接、请求代码片段以及 GET / POST 在线调试。</p>
            </div>
            <button
              type='button'
              className='mujian-docs-download'
              disabled={!runtimeSpec}
              onClick={downloadSpec}
            >
              <Download size={15} /> 下载 OpenAPI JSON
            </button>
          </div>

          <div className='mujian-docs-swagger'>
            {specError ? (
              <div className='mujian-docs-reference-state' role='alert'>
                <ShieldAlert size={22} />
                <strong>{specError}</strong>
                <button
                  type='button'
                  onClick={() => setReloadKey((key) => key + 1)}
                >
                  重新加载
                </button>
              </div>
            ) : runtimeSpec ? (
              <Suspense
                fallback={
                  <div className='mujian-docs-reference-state' role='status'>
                    <Braces size={22} />
                    <strong>正在启动接口参考…</strong>
                  </div>
                }
              >
                <SwaggerReference spec={runtimeSpec} />
              </Suspense>
            ) : (
              <div className='mujian-docs-reference-state' role='status'>
                <BookOpen size={22} />
                <strong>正在加载 OpenAPI 文档…</strong>
              </div>
            )}
          </div>
        </section>

        <section
          className='mujian-docs-unsupported'
          aria-labelledby='unsupported-title'
        >
          <div>
            <Terminal size={18} />
            <h2 id='unsupported-title'>暂不支持 / 待修复</h2>
          </div>
          <p>
            Files、Fine-tunes、删除模型、RelayNotImplemented 占位路由与内部
            Playground 不在公开可调试参考中。
            <code>/v1/engines/&#123;model&#125;/embeddings</code>
            存在契约不一致，请改用 <code>/v1/embeddings</code>。 Midjourney{' '}
            <code>/submit/edits</code>{' '}
            尚无可验证的公开请求契约，暂不提供在线调试。
          </p>
        </section>
      </div>
    </main>
  );
};

export default Docs;
