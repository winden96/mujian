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

import React, { useCallback, useEffect, useState } from 'react';
import {
  Banner,
  Button,
  Card,
  Input,
  Spin,
  Switch,
  Tag,
} from '@douyinfe/semi-ui';
import { KeyRound, RefreshCw, ShieldCheck, TestTube2 } from 'lucide-react';
import { API, showError, showSuccess } from '../../../helpers';
import './mujian-provider-panel.css';

const formatTime = (timestamp) => {
  if (!timestamp) return '尚未同步';
  return new Date(timestamp * 1000).toLocaleString('zh-CN', {
    hour12: false,
  });
};

const MujianProviderPanel = ({ onChannelsChanged }) => {
  const [providers, setProviders] = useState([]);
  const [keys, setKeys] = useState({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState('');

  const loadProviders = useCallback(async () => {
    try {
      const response = await API.get('/api/mujian/admin/providers');
      if (!response.data.success) throw new Error(response.data.message);
      setProviders(response.data.data || []);
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || '读取供应商失败',
      );
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadProviders();
  }, [loadProviders]);

  const run = async (provider, action, request) => {
    const operation = `${provider.id}:${action}`;
    setBusy(operation);
    try {
      const response = await request();
      if (!response.data.success) throw new Error(response.data.message);
      if (action === 'save') {
        setKeys((current) => ({ ...current, [provider.id]: '' }));
        showSuccess(`${provider.name} 密钥已保存`);
      } else if (action === 'sync') {
        showSuccess(
          `${provider.name} 已同步 ${response.data.data?.model_count || 0} 个精选模型`,
        );
      } else if (action === 'test') {
        showSuccess(
          `${provider.name} 连接正常，${response.data.data?.latency_ms || 0} ms`,
        );
      }
      await loadProviders();
      onChannelsChanged?.();
    } catch (error) {
      showError(error.response?.data?.message || error.message || '操作失败');
    } finally {
      setBusy('');
    }
  };

  return (
    <section
      className='mujian-provider-panel'
      aria-labelledby='mujian-provider-title'
    >
      <div className='mujian-provider-heading'>
        <div>
          <span className='mujian-provider-kicker'>MUJIAN ROUTING</span>
          <h2 id='mujian-provider-title'>创作供应商接入</h2>
          <p>
            密钥仅写入服务端渠道；同步后才会开放已验证且有明确价格的精选模型。
          </p>
        </div>
        <ShieldCheck aria-hidden='true' />
      </div>
      <Banner
        type='warning'
        closeIcon={null}
        description='此前通过聊天或其他非安全渠道发送过的密钥必须先轮换。这里不会回显完整密钥。'
      />
      <Spin spinning={loading}>
        <div className='mujian-provider-grid'>
          {providers.map((provider) => {
            const keyValue = keys[provider.id] || '';
            return (
              <Card
                key={provider.id}
                className='mujian-provider-card'
                bodyStyle={{ padding: 0 }}
              >
                <div className='mujian-provider-card-top'>
                  <div>
                    <div className='mujian-provider-name-row'>
                      <strong>{provider.name}</strong>
                      <Tag
                        color={
                          provider.enabled
                            ? 'green'
                            : provider.configured
                              ? 'orange'
                              : 'grey'
                        }
                      >
                        {provider.enabled
                          ? '已启用'
                          : provider.configured
                            ? '待同步'
                            : '未配置'}
                      </Tag>
                    </div>
                    <a
                      href={provider.site_url}
                      target='_blank'
                      rel='noreferrer'
                    >
                      查看供应商价格
                    </a>
                  </div>
                  <Switch
                    checked={provider.enabled}
                    disabled={!provider.configured || busy !== ''}
                    aria-label={`${provider.name}启用状态`}
                    onChange={(enabled) =>
                      run(provider, 'toggle', () =>
                        API.patch(
                          `/api/mujian/admin/providers/${provider.id}`,
                          { enabled },
                        ),
                      )
                    }
                  />
                </div>

                <div className='mujian-provider-key-row'>
                  <Input
                    mode='password'
                    prefix={<KeyRound size={15} />}
                    value={keyValue}
                    autoComplete='new-password'
                    placeholder={
                      provider.configured
                        ? `已配置 ····${provider.key_last4}`
                        : '粘贴轮换后的 API Key'
                    }
                    onChange={(value) =>
                      setKeys((current) => ({
                        ...current,
                        [provider.id]: value,
                      }))
                    }
                  />
                  <Button
                    theme='solid'
                    disabled={!keyValue.trim() || busy !== ''}
                    loading={busy === `${provider.id}:save`}
                    onClick={() =>
                      run(provider, 'save', () =>
                        API.put(`/api/mujian/admin/providers/${provider.id}`, {
                          key: keyValue.trim(),
                        }),
                      )
                    }
                  >
                    保存
                  </Button>
                </div>

                <dl className='mujian-provider-facts'>
                  <div>
                    <dt>精选模型</dt>
                    <dd>{provider.model_count || 0}</dd>
                  </div>
                  <div>
                    <dt>最近同步</dt>
                    <dd>{formatTime(provider.synced_at)}</dd>
                  </div>
                </dl>

                {provider.last_error && (
                  <p className='mujian-provider-error' role='alert'>
                    {provider.last_error}
                  </p>
                )}

                <div className='mujian-provider-actions'>
                  <Button
                    icon={<TestTube2 size={14} />}
                    disabled={!provider.configured || busy !== ''}
                    loading={busy === `${provider.id}:test`}
                    onClick={() =>
                      run(provider, 'test', () =>
                        API.post(
                          `/api/mujian/admin/providers/${provider.id}/test`,
                        ),
                      )
                    }
                  >
                    测试连接
                  </Button>
                  <Button
                    type='primary'
                    icon={<RefreshCw size={14} />}
                    disabled={!provider.configured || busy !== ''}
                    loading={busy === `${provider.id}:sync`}
                    onClick={() =>
                      run(provider, 'sync', () =>
                        API.post(
                          `/api/mujian/admin/providers/${provider.id}/sync`,
                        ),
                      )
                    }
                  >
                    同步模型与价格
                  </Button>
                </div>
              </Card>
            );
          })}
        </div>
      </Spin>
    </section>
  );
};

export default MujianProviderPanel;
