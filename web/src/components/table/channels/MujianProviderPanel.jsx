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
  Modal,
  Spin,
  Switch,
  Tag,
} from '@douyinfe/semi-ui';
import { KeyRound, RefreshCw, ShieldCheck, TestTube2 } from 'lucide-react';
import { API, showError, showSuccess } from '../../../helpers';
import './mujian-provider-panel.css';
import YuYuPricingImport from './YuYuPricingImport';

const formatTime = (timestamp) => {
  const seconds = Number(timestamp);
  if (!Number.isFinite(seconds) || seconds <= 0) return '尚无记录';
  return new Date(seconds * 1000).toLocaleString('zh-CN', {
    hour12: false,
  });
};

const priceFormatter = new Intl.NumberFormat('en-US', {
  maximumFractionDigits: 12,
});

const formatMoney = (value, currency = 'USD') => {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return '—';

  const normalizedCurrency = (currency || 'USD').toUpperCase();
  const formattedAmount = priceFormatter.format(amount);
  return normalizedCurrency === 'USD'
    ? `USD $${formattedAmount}`
    : `${normalizedCurrency} ${formattedAmount}`;
};

const safeExternalUrl = (value) => {
  try {
    const url = new URL(value);
    return ['http:', 'https:'].includes(url.protocol) ? url.href : '';
  } catch {
    return '';
  }
};

const channelStates = {
  1: { label: '已启用', color: 'green' },
  2: { label: '手动禁用', color: 'orange' },
  3: { label: '自动禁用', color: 'red' },
};

const getChannelState = (status) =>
  channelStates[status] || { label: '未知状态', color: 'grey' };

const getProviderState = (provider) => {
  if (provider.enabled) return { label: '已启用', color: 'green' };
  if (!provider.configured) return { label: '未配置', color: 'grey' };
  if (provider.last_error) return { label: '需处理', color: 'red' };
  if (!provider.synced_at) return { label: '待同步', color: 'orange' };
  if (!provider.tested_at) return { label: '待鉴权测试', color: 'orange' };
  return { label: '待启用', color: 'blue' };
};

const getPriceAvailability = (price) => {
  if (price.available) return { label: '可用', color: 'green' };
  if (price.last_error) return { label: '不可用', color: 'red' };
  return { label: '待验证', color: 'orange' };
};

const getBillingLabel = (price) => {
  if (price.billing_type === 'token') return 'Token';
  if (price.billing_type === 'fixed') return '按次';
  return price.billing_type || '未知计费';
};

const getRouteGroups = (provider) => [
  ...new Set(
    (Array.isArray(provider.routes) ? provider.routes : [])
      .map((route) => route.group)
      .filter(Boolean),
  ),
];

const getInitialGroupValue = (provider) => {
  const routeGroups = getRouteGroups(provider);
  if (routeGroups.length === 0) {
    return provider.strict_lifecycle ? '' : 'default';
  }
  if (routeGroups.length === 1) return routeGroups[0];
  return '';
};

const PriceDetails = ({ price }) => {
  const isTokenBilling = price.billing_type === 'token';
  const isFixedBilling = price.billing_type === 'fixed';
  const sourceUrl = safeExternalUrl(price.source_url);
  const availability = getPriceAvailability(price);

  return (
    <article className='mujian-provider-price'>
      <div className='mujian-provider-price-heading'>
        <div className='mujian-provider-mapping'>
          <span>模型映射</span>
          <div>
            <code>{price.catalog_id || '—'}</code>
            <span className='mujian-provider-map-arrow' aria-label='映射到'>
              →
            </span>
            <code>{price.upstream_model_id || '—'}</code>
          </div>
        </div>
        <div className='mujian-provider-price-tags'>
          <Tag color={availability.color}>{availability.label}</Tag>
          <Tag color='grey'>{getBillingLabel(price)}</Tag>
        </div>
      </div>

      {isTokenBilling && (
        <dl
          className='mujian-provider-price-values'
          aria-label={`${price.catalog_id || '模型'} Token 价格`}
        >
          <div>
            <dt>输入</dt>
            <dd>
              {formatMoney(price.input_price, price.currency)} / 百万 Token
            </dd>
          </div>
          <div>
            <dt>输出</dt>
            <dd>
              {formatMoney(price.output_price, price.currency)} / 百万 Token
            </dd>
          </div>
          {price.cache_read_price != null && (
            <div>
              <dt>缓存读取</dt>
              <dd>
                {formatMoney(price.cache_read_price, price.currency)} / 百万
                Token
              </dd>
            </div>
          )}
          {price.cache_write_5m_price != null && (
            <div>
              <dt>5 分钟缓存写入</dt>
              <dd>
                {formatMoney(price.cache_write_5m_price, price.currency)} / 百万
                Token
              </dd>
            </div>
          )}
          {price.cache_write_1h_price != null && (
            <div>
              <dt>1 小时缓存写入</dt>
              <dd>
                {formatMoney(price.cache_write_1h_price, price.currency)} / 百万
                Token
              </dd>
            </div>
          )}
        </dl>
      )}

      {isFixedBilling && (
        <dl className='mujian-provider-price-values is-fixed'>
          <div>
            <dt>固定单价</dt>
            <dd>{formatMoney(price.fixed_price, price.currency)} / 次</dd>
          </div>
        </dl>
      )}

      {!isTokenBilling && !isFixedBilling && (
        <p className='mujian-provider-price-unknown'>
          当前计费类型没有可展示的单价。
        </p>
      )}

      <div className='mujian-provider-price-meta'>
        <span>同步：{formatTime(price.synced_at)}</span>
        <span>测试：{formatTime(price.tested_at)}</span>
        {sourceUrl ? (
          <a href={sourceUrl} target='_blank' rel='noreferrer'>
            安全价格来源
          </a>
        ) : (
          <span>价格来源：未提供</span>
        )}
        <span className='mujian-provider-source-version'>
          版本：{price.source_version || '—'}
        </span>
      </div>

      {price.last_error && (
        <p className='mujian-provider-price-error' role='alert'>
          {price.last_error}
        </p>
      )}
    </article>
  );
};

const RouteDetails = ({ route }) => {
  const state = getChannelState(route.status);
  const prices = Array.isArray(route.prices) ? route.prices : [];

  return (
    <section
      className='mujian-provider-route'
      aria-labelledby={`mujian-provider-route-${route.channel_id}`}
    >
      <div className='mujian-provider-route-heading'>
        <div>
          <span className='mujian-provider-route-kicker'>
            ROUTE {route.profile_id || 'default'}
          </span>
          <h4 id={`mujian-provider-route-${route.channel_id}`}>
            {route.name || `渠道 #${route.channel_id}`}
          </h4>
        </div>
        <Tag color={state.color}>{state.label}</Tag>
      </div>

      <dl className='mujian-provider-route-facts'>
        <div>
          <dt>配置</dt>
          <dd>
            <code>{route.profile_id || 'default'}</code>
          </dd>
        </div>
        <div>
          <dt>分组</dt>
          <dd>
            <code>{route.group || '—'}</code>
          </dd>
        </div>
        <div>
          <dt>优先级</dt>
          <dd>{route.priority ?? '—'}</dd>
        </div>
        <div>
          <dt>渠道 ID</dt>
          <dd>#{route.channel_id}</dd>
        </div>
      </dl>

      <div className='mujian-provider-price-list'>
        {prices.length > 0 ? (
          prices.map((price) => (
            <PriceDetails
              key={`${route.channel_id}:${price.catalog_id}:${price.upstream_model_id}`}
              price={price}
            />
          ))
        ) : (
          <p className='mujian-provider-empty'>尚未同步模型映射与价格。</p>
        )}
      </div>
    </section>
  );
};

const MujianProviderPanel = ({ onChannelsChanged }) => {
  const [providers, setProviders] = useState([]);
  const [keys, setKeys] = useState({});
  const [groupDrafts, setGroupDrafts] = useState({});
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [busy, setBusy] = useState('');

  const loadProviders = useCallback(async (acceptServerGroupFor = '') => {
    try {
      const response = await API.get('/api/mujian/admin/providers');
      if (!response.data.success) throw new Error(response.data.message);
      const loadedProviders = response.data.data || [];
      setProviders(loadedProviders);
      setLoadError('');
      setGroupDrafts((current) =>
        Object.fromEntries(
          loadedProviders.map((provider) => {
            const existing = current[provider.id];
            if (provider.id !== acceptServerGroupFor && existing?.dirty) {
              return [provider.id, existing];
            }
            return [
              provider.id,
              { value: getInitialGroupValue(provider), dirty: false },
            ];
          }),
        ),
      );
    } catch (error) {
      const message =
        error.response?.data?.message || error.message || '读取供应商失败';
      setLoadError(message);
      showError(message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadProviders();
  }, [loadProviders]);

  const retryProviders = () => {
    setLoading(true);
    loadProviders();
  };

  const run = async (provider, action, request) => {
    const operation = `${provider.id}:${action}`;
    let succeeded = false;
    setBusy(operation);
    try {
      const response = await request();
      if (!response.data.success) throw new Error(response.data.message);
      succeeded = true;
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
      } else if (action === 'group') {
        showSuccess(`${provider.name} 路由分组已更新`);
      }
    } catch (error) {
      showError(error.response?.data?.message || error.message || '操作失败');
    } finally {
      const reloadSavedGroup =
        succeeded &&
        (action === 'group' || (action === 'save' && !provider.configured));
      await loadProviders(reloadSavedGroup ? provider.id : '');
      onChannelsChanged?.();
      setBusy('');
    }
  };

  const syncProvider = (provider) => {
    const request = () =>
      run(provider, 'sync', () =>
        API.post(
          `/api/mujian/admin/providers/${provider.id}/sync`,
          {},
          { skipErrorHandler: true },
        ),
      );
    if (!provider.strict_lifecycle || !provider.enabled) return request();
    Modal.confirm({
      title: `同步 ${provider.name} 并暂停当前路由？`,
      content:
        '同步会立即禁用该供应商的已启用路由并清除旧的鉴权测试状态。同步后必须重新完成鉴权测试和启用。',
      okText: '继续同步',
      cancelText: '取消',
      onOk: request,
    });
  };

  const testProvider = (provider) => {
    const request = () =>
      run(provider, 'test', () =>
        API.post(
          `/api/mujian/admin/providers/${provider.id}/test`,
          {},
          { skipErrorHandler: true },
        ),
      );
    if (!provider.strict_lifecycle || !provider.enabled) return request();
    Modal.confirm({
      title: `重新测试 ${provider.name} 的在线路由？`,
      content:
        '测试失败会立即禁用该供应商路由并使价格失效，需要重新同步、鉴权测试和启用后才能恢复流量。',
      okText: '继续测试',
      cancelText: '取消',
      onOk: request,
    });
  };

  const saveProviderKey = (provider, keyValue, groupValue) => {
    const request = () =>
      run(provider, 'save', () =>
        API.put(
          `/api/mujian/admin/providers/${provider.id}`,
          provider.configured
            ? { key: keyValue.trim() }
            : { key: keyValue.trim(), group: groupValue.trim() },
          { skipErrorHandler: true },
        ),
      );
    if (
      !provider.configured ||
      !provider.strict_lifecycle ||
      !provider.enabled
    ) {
      return request();
    }
    Modal.confirm({
      title: `轮换 ${provider.name} 密钥并暂停当前路由？`,
      content:
        '保存新密钥会立即禁用该供应商路由，并清除旧的同步与鉴权测试状态。轮换后必须重新同步、鉴权测试和启用。',
      okText: '继续轮换',
      cancelText: '取消',
      onOk: request,
    });
  };

  const saveProviderGroup = (provider, groupValue) => {
    const routeGroups = getRouteGroups(provider);
    const targetGroup = groupValue.trim();
    const request = () =>
      run(provider, 'group', () =>
        API.patch(
          `/api/mujian/admin/providers/${provider.id}`,
          { group: targetGroup },
          { skipErrorHandler: true },
        ),
      );
    if (routeGroups.length > 1) {
      Modal.confirm({
        title: `统一 ${provider.name} 的路由分组？`,
        content: `当前路由分布在多个分组。继续后将全部移到 ${targetGroup}。`,
        okText: '统一分组',
        cancelText: '取消',
        onOk: request,
      });
      return;
    }
    if (provider.enabled && routeGroups[0] && routeGroups[0] !== targetGroup) {
      Modal.confirm({
        title: `移动 ${provider.name} 的已启用路由？`,
        content: `路由会立即从 ${routeGroups[0]} 移到 ${targetGroup}，并随即移出原分组流量、承接目标分组流量。`,
        okText: '继续移动',
        cancelText: '取消',
        onOk: request,
      });
      return;
    }
    return request();
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
      {loadError && (
        <Banner
          type='danger'
          closeIcon={null}
          title='暂时无法读取供应商配置'
          description={
            <div>
              <span>{loadError}</span>
              <Button
                size='small'
                loading={loading}
                disabled={loading || busy !== ''}
                onClick={retryProviders}
              >
                重试
              </Button>
            </div>
          }
        />
      )}
      <Spin spinning={loading}>
        {!loadError && (
          <div className='mujian-provider-grid'>
            {providers.map((provider) => {
              const keyValue = keys[provider.id] || '';
              const groupValue =
                groupDrafts[provider.id]?.value ??
                getInitialGroupValue(provider);
              const providerState = getProviderState(provider);
              const providerRoutes = Array.isArray(provider.routes)
                ? provider.routes
                : [];
              const hasMixedGroups = getRouteGroups(provider).length > 1;
              const siteUrl = safeExternalUrl(provider.site_url);
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
                        <Tag color={providerState.color}>
                          {providerState.label}
                        </Tag>
                      </div>
                      {siteUrl && (
                        <a href={siteUrl} target='_blank' rel='noreferrer'>
                          查看供应商站点
                        </a>
                      )}
                    </div>
                    <span className='mujian-provider-route-count'>
                      {providerRoutes.length} 条路由
                    </span>
                  </div>

                  <div className='mujian-provider-key-row'>
                    <Input
                      mode='password'
                      prefix={<KeyRound size={15} />}
                      value={keyValue}
                      disabled={busy !== ''}
                      autoComplete='new-password'
                      placeholder={
                        provider.configured
                          ? '密钥已安全保存；输入新值可轮换'
                          : '粘贴轮换后的 API Key'
                      }
                      aria-label={`${provider.name} API Key`}
                      onChange={(value) =>
                        setKeys((current) => ({
                          ...current,
                          [provider.id]: value,
                        }))
                      }
                    />
                    <Button
                      theme='solid'
                      disabled={
                        !keyValue.trim() ||
                        (!provider.configured && !groupValue.trim()) ||
                        busy !== ''
                      }
                      loading={busy === `${provider.id}:save`}
                      onClick={() =>
                        saveProviderKey(provider, keyValue, groupValue)
                      }
                    >
                      保存
                    </Button>
                  </div>

                  {provider.id === 'yuyu' && (
                    <YuYuPricingImport
                      disabled={!provider.configured || busy !== ''}
                      onImported={async () => {
                        await loadProviders();
                        onChannelsChanged?.();
                      }}
                    />
                  )}
                  <div className='mujian-provider-group-row'>
                    <label>
                      <span>
                        路由分组
                        {hasMixedGroups && <Tag color='orange'>当前混合</Tag>}
                      </span>
                      <Input
                        value={groupValue}
                        disabled={busy !== ''}
                        placeholder={
                          hasMixedGroups
                            ? '输入要统一到的目标分组'
                            : '例如 mujian-canary 或 default'
                        }
                        aria-label={`${provider.name}路由分组`}
                        onChange={(value) =>
                          setGroupDrafts((current) => ({
                            ...current,
                            [provider.id]: { value, dirty: true },
                          }))
                        }
                      />
                    </label>
                    <Button
                      disabled={
                        !provider.configured ||
                        !groupValue.trim() ||
                        !groupDrafts[provider.id]?.dirty ||
                        busy !== ''
                      }
                      loading={busy === `${provider.id}:group`}
                      onClick={() => saveProviderGroup(provider, groupValue)}
                    >
                      保存分组
                    </Button>
                  </div>

                  <dl className='mujian-provider-facts'>
                    <div>
                      <dt>可用模型</dt>
                      <dd>{provider.model_count || 0}</dd>
                    </div>
                    <div>
                      <dt>最近同步</dt>
                      <dd>{formatTime(provider.synced_at)}</dd>
                    </div>
                    <div>
                      <dt>最近鉴权测试</dt>
                      <dd>{formatTime(provider.tested_at)}</dd>
                    </div>
                  </dl>

                  {provider.last_error && (
                    <p className='mujian-provider-error' role='alert'>
                      {provider.last_error}
                    </p>
                  )}

                  <div className='mujian-provider-route-list'>
                    {providerRoutes.length > 0 ? (
                      providerRoutes.map((route) => (
                        <RouteDetails
                          key={`${provider.id}:${route.channel_id}:${route.profile_id}`}
                          route={route}
                        />
                      ))
                    ) : (
                      <p className='mujian-provider-empty'>
                        保存密钥后将创建供应商路由。
                      </p>
                    )}
                  </div>

                  <div className='mujian-provider-actions'>
                    <Button
                      type='primary'
                      icon={<RefreshCw size={14} />}
                      disabled={!provider.configured || busy !== ''}
                      loading={busy === `${provider.id}:sync`}
                      onClick={() => syncProvider(provider)}
                    >
                      同步模型与价格
                    </Button>
                    <Button
                      icon={<TestTube2 size={14} />}
                      disabled={!provider.test_ready || busy !== ''}
                      loading={busy === `${provider.id}:test`}
                      onClick={() => testProvider(provider)}
                    >
                      鉴权测试
                    </Button>
                    <label className='mujian-provider-enable-control'>
                      <span>启用</span>
                      <Switch
                        checked={provider.enabled}
                        disabled={
                          !provider.configured ||
                          (!provider.enabled && !provider.enable_ready) ||
                          busy !== ''
                        }
                        aria-label={`${provider.name}启用状态`}
                        onChange={(enabled) =>
                          run(provider, 'toggle', () =>
                            API.patch(
                              `/api/mujian/admin/providers/${provider.id}`,
                              { enabled },
                              { skipErrorHandler: true },
                            ),
                          )
                        }
                      />
                    </label>
                  </div>
                </Card>
              );
            })}
          </div>
        )}
      </Spin>
    </section>
  );
};

export default MujianProviderPanel;
