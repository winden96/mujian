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

import React, { useContext, useEffect, useMemo, useState } from 'react';
import { Banner, Button, Card, Tag } from '@douyinfe/semi-ui';
import {
  CheckCircle2,
  Copy,
  KeyRound,
  MonitorSmartphone,
  Server,
  ShieldCheck,
} from 'lucide-react';
import {
  API,
  copy,
  resolveApiBaseURL,
  resolveGatewayAddress,
  showError,
  showSuccess,
} from '../../helpers';
import { encodeToBase64 } from '../../helpers/base64';
import { StatusContext } from '../../context/Status';
import '../mujian.css';

const MujianIntegrations = () => {
  const [statusState] = useContext(StatusContext);
  const [config, setConfig] = useState(null);
  const [loadingAction, setLoadingAction] = useState('load');
  const [loadError, setLoadError] = useState('');
  const [revealed, setRevealed] = useState(false);
  const gatewayAddress = resolveGatewayAddress(
    statusState?.status?.server_address,
    window.location.origin,
  );
  const apiBaseURL = resolveApiBaseURL(
    statusState?.status?.server_address,
    window.location.origin,
  );

  const maskedKey = useMemo(() => {
    if (config) return `sk-••••••••••${config.token_last4}`;
    return loadError ? '当前不可用' : '正在生成…';
  }, [config, loadError]);

  const loadConfig = async (force = false) => {
    if (config && !force) return config;
    try {
      const response = await API.post('/api/mujian/integrations/cherry-studio');
      if (!response.data.success) throw new Error(response.data.message);
      setConfig(response.data.data);
      setLoadError('');
      return response.data.data;
    } catch (error) {
      const message =
        error.response?.data?.message || error.message || '生成接入配置失败';
      setConfig(null);
      setRevealed(false);
      setLoadError(message);
      throw error;
    }
  };

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        await loadConfig();
      } catch (error) {
        if (!cancelled) {
          showError(
            error.response?.data?.message ||
              error.message ||
              '生成 API Key 失败',
          );
        }
      } finally {
        if (!cancelled) setLoadingAction('');
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const runAction = async (name, action, forceConfig = false) => {
    setLoadingAction(name);
    try {
      await action(await loadConfig(forceConfig));
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || '生成接入配置失败',
      );
    } finally {
      setLoadingAction('');
    }
  };

  const retryConfig = () => runAction('load', () => undefined, true);
  const actionsDisabled = loadingAction !== '' || Boolean(loadError);

  const copyValue = (value, label) =>
    runAction(
      `copy-${label}`,
      async (loadedConfig) => {
        const copied = await copy(
          value === 'api_key' ? loadedConfig.api_key : value,
        );
        if (copied) showSuccess(`${label}已复制`);
      },
      value === 'api_key',
    );

  const importCherryStudio = () =>
    runAction(
      'import',
      async (loadedConfig) => {
        const payload = encodeToBase64(
          JSON.stringify({
            id: 'new-api',
            baseUrl: gatewayAddress,
            apiKey: loadedConfig.api_key,
          }),
        );
        window.location.href = `cherrystudio://providers/api-keys?v=1&data=${encodeURIComponent(payload)}`;
      },
      true,
    );

  return (
    <main className='mujian-page mujian-integrations-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>
            <MonitorSmartphone size={14} /> OpenAI Compatible
          </div>
          <h1>API Key</h1>
          <p>
            复制你的幕间受限 Token。调用文档里的接口时，用它做 Bearer 认证。
          </p>
        </div>
      </section>

      {loadError && (
        <Banner
          type='danger'
          closeIcon={null}
          title='暂时无法读取接入配置'
          description={
            <div>
              <span>{loadError}</span>
              <Button
                size='small'
                loading={loadingAction === 'load'}
                disabled={loadingAction !== ''}
                onClick={retryConfig}
              >
                重试
              </Button>
            </div>
          }
        />
      )}

      <section className='mujian-integration-layout'>
        <Card className='mujian-client-card'>
          <div className='mujian-client-heading'>
            <div className='mujian-client-mark'>
              <KeyRound size={20} />
            </div>
            <div>
              <div className='mujian-client-title-row'>
                <h2>你的密钥</h2>
                <Tag color='orange'>受限 Token</Tag>
              </div>
              <p>登录时已签发。不会暴露任何上游渠道密钥。</p>
            </div>
          </div>

          <div className='mujian-connection-fields'>
            <div className='mujian-connection-field'>
              <span>
                <Server size={14} /> API Base URL
              </span>
              <code>{apiBaseURL}</code>
              <Button
                aria-label='复制 API Base URL'
                icon={<Copy size={15} />}
                theme='borderless'
                disabled={actionsDisabled}
                onClick={() => copyValue(apiBaseURL, 'Base URL')}
              />
            </div>
            <div className='mujian-connection-field'>
              <span>
                <KeyRound size={14} /> API Key
              </span>
              <code>{revealed && config ? config.api_key : maskedKey}</code>
              <Button
                aria-label='复制 API Key'
                icon={<Copy size={15} />}
                theme='borderless'
                loading={
                  loadingAction === 'copy-Token' || loadingAction === 'load'
                }
                disabled={actionsDisabled}
                onClick={() => copyValue('api_key', 'Token')}
              />
            </div>
          </div>

          <div className='mujian-client-actions'>
            <Button
              theme='solid'
              size='large'
              icon={<Copy size={17} />}
              loading={
                loadingAction === 'copy-Token' || loadingAction === 'load'
              }
              disabled={actionsDisabled}
              onClick={() => copyValue('api_key', 'Token')}
            >
              复制 API Key
            </Button>
            <Button
              size='large'
              onClick={() => setRevealed((value) => !value)}
              disabled={!config || actionsDisabled}
            >
              {revealed ? '隐藏密钥' : '显示密钥'}
            </Button>
          </div>
          <p className='mujian-client-action-note'>
            把密钥当作密码保管。请求头使用 Authorization: Bearer sk-…
          </p>
        </Card>

        <aside className='mujian-integration-aside'>
          <div className='mujian-security-note'>
            <ShieldCheck size={20} />
            <div>
              <strong>不暴露渠道密钥</strong>
              <p>
                这是你本人的幕间受限 Token，不是任何上游供应商的管理员密钥。
              </p>
            </div>
          </div>
          <ol className='mujian-manual-steps'>
            <li>
              <span>1</span>
              <div>
                <strong>复制上方 Base URL 和 API Key</strong>
                <p>
                  Base URL 需保留 <code>/v1</code>。
                </p>
              </div>
            </li>
            <li>
              <span>2</span>
              <div>
                <strong>按文档发起第一次请求</strong>
                <p>先 GET /v1/models，再用同一 Token 调目标接口。</p>
              </div>
            </li>
            <li>
              <span>3</span>
              <div>
                <strong>可选：导入桌面客户端</strong>
                <p>已安装 Cherry Studio 时，可一键写入网关和密钥。</p>
              </div>
            </li>
          </ol>
          <Button
            className='mujian-cherry-import'
            icon={<MonitorSmartphone size={16} />}
            loading={loadingAction === 'import'}
            disabled={actionsDisabled}
            onClick={importCherryStudio}
          >
            一键导入 Cherry Studio
          </Button>
        </aside>
      </section>

      <section className='mujian-connected-models'>
        <div className='mujian-connected-models-heading'>
          <div>
            <span>AVAILABLE MODELS</span>
            <h2>可接入对话模型</h2>
          </div>
          <strong>
            {config
              ? `${config.models.length} 个`
              : loadError
                ? '不可用'
                : '加载中'}
          </strong>
        </div>
        {config?.default_model_available === false && (
          <Banner
            type='warning'
            closeIcon={null}
            description={`已保留你显式选择的默认模型 ${config.default_model}，但它当前在所属分组不可用。请切换模型或联系管理员恢复路由。`}
          />
        )}
        {config ? (
          <div className='mujian-connected-model-list'>
            {config.models.map((model) => (
              <div key={model}>
                <CheckCircle2 size={15} />
                <code>{model}</code>
                {model === config.default_model && (
                  <Tag color='orange'>默认</Tag>
                )}
              </div>
            ))}
          </div>
        ) : (
          <div className='mujian-integration-empty'>
            {loadError
              ? '接入配置恢复后显示当前可用模型。'
              : '密钥生成后显示当前可用模型。'}
          </div>
        )}
      </section>
    </main>
  );
};

export default MujianIntegrations;
