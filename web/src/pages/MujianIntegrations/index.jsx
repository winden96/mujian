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

import React, { useMemo, useState } from 'react';
import { Button, Card, Tag } from '@douyinfe/semi-ui';
import {
  CheckCircle2,
  Copy,
  KeyRound,
  MonitorSmartphone,
  Server,
  ShieldCheck,
} from 'lucide-react';
import { API, copy, showError, showSuccess } from '../../helpers';
import { encodeToBase64 } from '../../helpers/base64';
import '../mujian.css';

const MujianIntegrations = () => {
  const [config, setConfig] = useState(null);
  const [loadingAction, setLoadingAction] = useState('');
  const gatewayOrigin = window.location.origin;
  const apiBaseURL = `${gatewayOrigin}/v1`;

  const maskedKey = useMemo(
    () => (config ? `sk-••••••••••${config.token_last4}` : '点击后生成'),
    [config],
  );

  const loadConfig = async () => {
    if (config) return config;
    const response = await API.post('/api/mujian/integrations/cherry-studio');
    setConfig(response.data.data);
    return response.data.data;
  };

  const runAction = async (name, action) => {
    setLoadingAction(name);
    try {
      await action(await loadConfig());
    } catch (error) {
      showError(error.response?.data?.message || '生成接入配置失败');
    } finally {
      setLoadingAction('');
    }
  };

  const copyValue = (value, label) =>
    runAction(`copy-${label}`, async (loadedConfig) => {
      const copied = await copy(
        value === 'api_key' ? loadedConfig.api_key : value,
      );
      if (copied) showSuccess(`${label}已复制`);
    });

  const importCherryStudio = () =>
    runAction('import', async (loadedConfig) => {
      const payload = encodeToBase64(
        JSON.stringify({
          id: 'new-api',
          baseUrl: gatewayOrigin,
          apiKey: loadedConfig.api_key,
        }),
      );
      window.location.href = `cherrystudio://providers/api-keys?v=1&data=${encodeURIComponent(payload)}`;
    });

  return (
    <main className='mujian-page mujian-integrations-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>
            <MonitorSmartphone size={14} /> OpenAI Compatible
          </div>
          <h1>客户端接入</h1>
          <p>把幕间 AI 的精选对话模型安全接入桌面客户端。</p>
        </div>
      </section>

      <section className='mujian-integration-layout'>
        <Card className='mujian-client-card'>
          <div className='mujian-client-heading'>
            <div className='mujian-client-mark'>CS</div>
            <div>
              <div className='mujian-client-title-row'>
                <h2>Cherry Studio</h2>
                <Tag color='green'>已支持</Tag>
              </div>
              <p>一键写入幕间网关和用户 Token。</p>
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
                onClick={() => copyValue(apiBaseURL, 'Base URL')}
              />
            </div>
            <div className='mujian-connection-field'>
              <span>
                <KeyRound size={14} /> API Key
              </span>
              <code>{maskedKey}</code>
              <Button
                aria-label='复制 API Key'
                icon={<Copy size={15} />}
                theme='borderless'
                loading={loadingAction === 'copy-Token'}
                onClick={() => copyValue('api_key', 'Token')}
              />
            </div>
          </div>

          <div className='mujian-client-actions'>
            <Button
              theme='solid'
              size='large'
              icon={<MonitorSmartphone size={17} />}
              loading={loadingAction === 'import'}
              onClick={importCherryStudio}
            >
              一键导入 Cherry Studio
            </Button>
            <Button
              size='large'
              loading={loadingAction === 'prepare'}
              onClick={() =>
                runAction('prepare', async () => {
                  showSuccess('配置已生成，可复制 Token');
                })
              }
            >
              生成手动配置
            </Button>
          </div>
          <p className='mujian-client-action-note'>
            需先在本机安装并打开 Cherry Studio；导入后请在客户端同步并选择模型。
          </p>
        </Card>

        <aside className='mujian-integration-aside'>
          <div className='mujian-security-note'>
            <ShieldCheck size={20} />
            <div>
              <strong>不暴露渠道密钥</strong>
              <p>
                Cherry Studio 获取的是你本人的幕间受限 Token，不是云雾或 GeekNow
                的管理员密钥。
              </p>
            </div>
          </div>
          <ol className='mujian-manual-steps'>
            <li>
              <span>1</span>
              <div>
                <strong>添加 OpenAI 兼容服务</strong>
                <p>在 Cherry Studio 的模型服务中添加自定义服务。</p>
              </div>
            </li>
            <li>
              <span>2</span>
              <div>
                <strong>填入上方地址与 Token</strong>
                <p>
                  Base URL 需保留 <code>/v1</code>。
                </p>
              </div>
            </li>
            <li>
              <span>3</span>
              <div>
                <strong>同步并选择模型</strong>
                <p>只会显示管理员已开通、已配价的精选模型。</p>
              </div>
            </li>
          </ol>
        </aside>
      </section>

      <section className='mujian-connected-models'>
        <div className='mujian-connected-models-heading'>
          <div>
            <span>AVAILABLE MODELS</span>
            <h2>可接入对话模型</h2>
          </div>
          <strong>
            {config ? `${config.models.length} 个` : '生成配置后显示'}
          </strong>
        </div>
        {config ? (
          <div className='mujian-connected-model-list'>
            {config.models.map((model) => (
              <div key={model}>
                <CheckCircle2 size={15} />
                <code>{model}</code>
                {model === config.default_model && (
                  <Tag color='violet'>默认</Tag>
                )}
              </div>
            ))}
          </div>
        ) : (
          <div className='mujian-integration-empty'>
            点击“生成手动配置”，即时读取当前可用模型。
          </div>
        )}
      </section>
    </main>
  );
};

export default MujianIntegrations;
