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

import React, { useContext, useEffect, useMemo, useRef, useState } from 'react';
import { Banner, Button, Input, Select, Spin, Tag } from '@douyinfe/semi-ui';
import {
  CircleAlert,
  CircleCheck,
  ChevronDown,
  Clock3,
  Image as ImageIcon,
  MessageSquareText,
  RefreshCw,
  Route,
  Search,
  SlidersHorizontal,
  Sparkles,
} from 'lucide-react';
import ModelPricingPage from '../../components/table/model-pricing/layout/PricingPage';
import { UserContext } from '../../context/User';
import { API, isAdmin, showError, showSuccess } from '../../helpers';
import '../mujian.css';

const EMPTY_MODELS = { chat: [], image: [], items: [] };
const MODEL_KIND_FILTERS = [
  ['all', '全部类型'],
  ['chat', '对话模型'],
  ['image', '图像模型'],
];
const MODEL_STATUS_FILTERS = [
  ['all', '全部状态'],
  ['available', '可用'],
  ['pending', '待开放'],
];

const normalizeModels = (value) => ({
  chat: Array.isArray(value?.chat) ? value.chat : [],
  image: Array.isArray(value?.image) ? value.image : [],
  items: Array.isArray(value?.items) ? value.items : [],
});

const Pricing = () => {
  const [userState] = useContext(UserContext);
  const [preference, setPreference] = useState(null);
  const [models, setModels] = useState(EMPTY_MODELS);
  const [search, setSearch] = useState('');
  const [kind, setKind] = useState('all');
  const [availability, setAvailability] = useState('all');
  const [catalogStatus, setCatalogStatus] = useState('loading');
  const [catalogError, setCatalogError] = useState('');
  const [catalogRequest, setCatalogRequest] = useState(0);
  const [savingDefaults, setSavingDefaults] = useState({});
  const preferenceUpdateVersions = useRef({});

  const showDefaults = Boolean(userState?.user) && !isAdmin();
  const showCatalog = !isAdmin();

  useEffect(() => {
    if (!showCatalog) return;

    let active = true;
    setCatalogStatus('loading');
    setCatalogError('');
    setPreference(null);
    setModels(EMPTY_MODELS);

    API.get(showDefaults ? '/api/mujian/preferences' : '/api/mujian/models')
      .then((response) => {
        if (!response.data.success) throw new Error(response.data.message);
        if (!active) return;
        setPreference(showDefaults ? response.data.data : null);
        setModels(
          normalizeModels(
            showDefaults ? response.data.models : response.data.data,
          ),
        );
        setCatalogStatus('ready');
      })
      .catch((error) => {
        if (!active) return;
        const message =
          error.response?.data?.message ||
          error.message ||
          '暂时无法读取模型配置';
        setCatalogError(message);
        setCatalogStatus('error');
      });

    return () => {
      active = false;
    };
  }, [catalogRequest, showDefaults, showCatalog]);

  const updateDefault = async (field, value) => {
    const version = (preferenceUpdateVersions.current[field] || 0) + 1;
    preferenceUpdateVersions.current[field] = version;
    setSavingDefaults((current) => ({ ...current, [field]: true }));
    try {
      const response = await API.put('/api/mujian/preferences', {
        [field]: value,
      });
      if (!response.data.success) throw new Error(response.data.message);
      if (preferenceUpdateVersions.current[field] !== version) return;
      setPreference((current) => ({
        ...current,
        [field]: response.data.data[field],
      }));
      showSuccess('默认模型已更新');
    } catch (error) {
      if (preferenceUpdateVersions.current[field] === version) {
        showError(error.response?.data?.message || error.message || '更新失败');
      }
    } finally {
      if (preferenceUpdateVersions.current[field] === version) {
        setSavingDefaults((current) => ({ ...current, [field]: false }));
      }
    }
  };

  const chatModelAvailable = models.chat.includes(
    preference?.default_chat_model,
  );
  const imageModelAvailable = models.image.includes(
    preference?.default_image_model,
  );
  const unavailableDefaults = [
    !chatModelAvailable && preference?.default_chat_model
      ? `对话模型 ${preference.default_chat_model}`
      : '',
    !imageModelAvailable && preference?.default_image_model
      ? `图像模型 ${preference.default_image_model}`
      : '',
  ].filter(Boolean);
  const hasUnavailableDefault =
    catalogStatus === 'ready' && unavailableDefaults.length > 0;
  const modelOptions = (values, currentValue, currentAvailable) => {
    const availableOptions = values.map((value) => ({ label: value, value }));
    if (!currentValue || currentAvailable) {
      return availableOptions;
    }
    return [
      {
        label: `${currentValue}（当前不可用）`,
        value: currentValue,
        disabled: true,
      },
      ...availableOptions,
    ];
  };
  const catalogItems = models.items;
  const availableItems = catalogItems.filter((item) => item.available);
  const availableRouteCount = availableItems.reduce(
    (total, item) => total + (Number(item.channel_count) || 0),
    0,
  );
  const catalogReady = catalogStatus === 'ready';
  const catalogLoading = catalogStatus === 'loading';
  const connected =
    catalogReady && Boolean(models.chat.length || models.image.length);
  let defaultStatus = connected ? '已连接' : '等待渠道配置';
  if (hasUnavailableDefault) defaultStatus = '默认模型不可用';
  if (catalogLoading) defaultStatus = '正在检查配置';
  if (catalogStatus === 'error') defaultStatus = '配置状态未知';

  const modelPlaceholder = (values, label) => {
    if (catalogLoading) return `正在加载${label}`;
    if (catalogStatus === 'error') return `暂时无法读取${label}`;
    return values.length ? `请选择${label}` : `暂无可用${label}`;
  };

  const retryCatalog = () => setCatalogRequest((value) => value + 1);
  const resetFilters = () => {
    setSearch('');
    setKind('all');
    setAvailability('all');
  };

  const filteredItems = useMemo(() => {
    const normalizedSearch = search.trim().toLowerCase();
    return catalogItems.filter((item) => {
      const matchesSearch =
        !normalizedSearch ||
        [item.name, item.id, item.provider_name, ...(item.tags || [])]
          .join(' ')
          .toLowerCase()
          .includes(normalizedSearch);
      const matchesKind = kind === 'all' || item.kind === kind;
      const matchesAvailability =
        availability === 'all' ||
        (availability === 'available' ? item.available : !item.available);
      return matchesSearch && matchesKind && matchesAvailability;
    });
  }, [availability, catalogItems, kind, search]);

  const filteredAvailableItems = filteredItems.filter((item) => item.available);
  const filteredPendingItems = filteredItems.filter((item) => !item.available);

  const money = (value, digits) => `$${(Number(value) || 0).toFixed(digits)}`;
  const priceRange = (min, max, digits) =>
    min === max
      ? money(min, digits)
      : `${money(min, digits)}–${money(max, digits)}`;
  const priceText = (item) => {
    if (item.billing_type === 'fixed') {
      const range = priceRange(item.min_fixed_price, item.max_fixed_price, 3);
      return `${range} / 次`;
    }
    const input = priceRange(item.min_input_price, item.max_input_price, 2);
    const output = priceRange(item.min_output_price, item.max_output_price, 2);
    return `输入 ${input} · 输出 ${output} / 1M`;
  };

  return (
    <div className='mujian-pricing-page'>
      {showCatalog && (
        <section
          className='mujian-model-market-hero'
          aria-labelledby='market-title'
        >
          <div className='mujian-model-market-intro'>
            <span className='mujian-market-eyebrow'>
              <Sparkles size={13} aria-hidden='true' />
              MUJIAN MODEL ROUTER
            </span>
            <h1 id='market-title'>模型广场</h1>
            <p>
              精选适合剧本、分镜与画面生成的模型。只有完成渠道验证和价格配置后，模型才会开放使用。
            </p>
            <div
              className='mujian-market-stats'
              aria-label='模型广场状态'
              aria-live='polite'
            >
              <span>
                <CircleCheck size={14} aria-hidden='true' />
                <strong>
                  {catalogReady ? availableItems.length : '—'}
                </strong>{' '}
                个可用模型
              </span>
              <span>
                <Route size={14} aria-hidden='true' />
                <strong>{catalogReady ? availableRouteCount : '—'}</strong>{' '}
                条可用路由
              </span>
              <span>{catalogReady ? catalogItems.length : '—'} 个精选候选</span>
            </div>
          </div>
          {showDefaults && (
            <div className='mujian-pricing-defaults' aria-label='默认创作模型'>
              <header>
                <div>
                  <strong>默认创作模型</strong>
                  <span>工作台将自动使用这里的选择</span>
                </div>
                <span
                  className={`mujian-default-status${connected && !hasUnavailableDefault ? ' ready' : ''}`}
                  role='status'
                >
                  {defaultStatus}
                </span>
              </header>
              {hasUnavailableDefault && (
                <Banner
                  type='warning'
                  closeIcon={null}
                  description={`已保留你显式选择的${unavailableDefaults.join('、')}，但它当前在所属分组不可用。请在下方显式选择可用模型。`}
                />
              )}
              <div className='mujian-default-model-fields'>
                <label>
                  <span>
                    <MessageSquareText size={14} aria-hidden='true' /> 对话模型
                  </span>
                  <Select
                    aria-label='默认对话模型'
                    value={preference?.default_chat_model}
                    placeholder={modelPlaceholder(models.chat, '对话模型')}
                    optionList={modelOptions(
                      models.chat,
                      preference?.default_chat_model,
                      chatModelAvailable,
                    )}
                    defaultActiveFirstOption={false}
                    arrowIcon={
                      <ChevronDown
                        size={15}
                        aria-hidden='true'
                        focusable='false'
                      />
                    }
                    loading={
                      catalogLoading || savingDefaults.default_chat_model
                    }
                    disabled={
                      !catalogReady ||
                      !models.chat.length ||
                      savingDefaults.default_chat_model
                    }
                    onChange={(value) =>
                      updateDefault('default_chat_model', value)
                    }
                  />
                </label>
                <label>
                  <span>
                    <ImageIcon size={14} aria-hidden='true' /> 图像模型
                  </span>
                  <Select
                    aria-label='默认图像模型'
                    value={preference?.default_image_model}
                    placeholder={modelPlaceholder(models.image, '图像模型')}
                    optionList={modelOptions(
                      models.image,
                      preference?.default_image_model,
                      imageModelAvailable,
                    )}
                    defaultActiveFirstOption={false}
                    arrowIcon={
                      <ChevronDown
                        size={15}
                        aria-hidden='true'
                        focusable='false'
                      />
                    }
                    loading={
                      catalogLoading || savingDefaults.default_image_model
                    }
                    disabled={
                      !catalogReady ||
                      !models.image.length ||
                      savingDefaults.default_image_model
                    }
                    onChange={(value) =>
                      updateDefault('default_image_model', value)
                    }
                  />
                </label>
              </div>
            </div>
          )}
        </section>
      )}
      {showCatalog ? (
        <section
          className='mujian-curated-models'
          aria-label='精选模型目录'
          aria-busy={catalogLoading}
        >
          <div className='mujian-model-toolbar'>
            <Input
              aria-label='搜索模型'
              aria-controls='mujian-model-results'
              prefix={<Search size={16} aria-hidden='true' />}
              placeholder='搜索模型名称、厂商或能力标签'
              value={search}
              showClear
              disabled={!catalogReady}
              onChange={setSearch}
            />
            <div
              className='mujian-model-filter-group'
              role='group'
              aria-label='模型类型筛选'
            >
              <SlidersHorizontal size={15} aria-hidden='true' />
              {MODEL_KIND_FILTERS.map(([value, label]) => (
                <button
                  key={value}
                  type='button'
                  className={kind === value ? 'active' : ''}
                  aria-pressed={kind === value}
                  aria-controls='mujian-model-results'
                  disabled={!catalogReady}
                  onClick={() => setKind(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div
              className='mujian-model-filter-group availability'
              role='group'
              aria-label='可用状态筛选'
            >
              {MODEL_STATUS_FILTERS.map(([value, label]) => (
                <button
                  key={value}
                  type='button'
                  className={availability === value ? 'active' : ''}
                  aria-pressed={availability === value}
                  aria-controls='mujian-model-results'
                  disabled={!catalogReady}
                  onClick={() => setAvailability(value)}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>

          <div id='mujian-model-results' aria-live='polite'>
            {catalogLoading ? (
              <div className='mujian-curated-empty' role='status'>
                <Spin size='middle' />
                <strong>正在读取模型配置</strong>
                <span>
                  正在核对渠道验证、模型同步和价格配置，完成前不会把模型标记为可用。
                </span>
              </div>
            ) : catalogStatus === 'error' ? (
              <div className='mujian-curated-empty' role='alert'>
                <CircleAlert size={20} aria-hidden='true' />
                <strong>暂时无法确认模型是否可用</strong>
                <span>
                  {catalogError}
                  。请重试；如果持续失败，请联系管理员检查渠道、模型同步与价格配置。
                </span>
                <Button
                  type='primary'
                  icon={<RefreshCw size={14} aria-hidden='true' />}
                  onClick={retryCatalog}
                >
                  重新加载模型状态
                </Button>
              </div>
            ) : filteredItems.length ? (
              <>
                {(availability === 'all' || availability === 'available') && (
                  <div className='mujian-model-section'>
                    <header>
                      <div>
                        <span className='mujian-model-section-kicker'>
                          READY
                        </span>
                        <h2>可用模型</h2>
                      </div>
                      <strong>{filteredAvailableItems.length}</strong>
                    </header>
                    {filteredAvailableItems.length ? (
                      <div className='mujian-curated-model-grid'>
                        {filteredAvailableItems.map((item) => (
                          <article
                            key={item.id}
                            className='mujian-curated-model-card'
                          >
                            <div className='mujian-curated-model-top'>
                              <span
                                className={`mujian-model-mark ${item.kind}`}
                              >
                                {item.kind === 'chat' ? 'AI' : 'IMG'}
                              </span>
                              <div>
                                <h3>{item.name}</h3>
                                <p>{item.provider_name}</p>
                              </div>
                              <span className='mujian-model-status available'>
                                可用
                              </span>
                            </div>
                            <div className='mujian-model-tags'>
                              {(item.tags || []).map((tag) => (
                                <Tag key={tag}>{tag}</Tag>
                              ))}
                            </div>
                            <div className='mujian-model-price'>
                              {priceText(item)}
                            </div>
                            <footer>
                              <span>{item.channel_count} 条容灾路由</span>
                              <span>{(item.providers || []).join(' / ')}</span>
                            </footer>
                          </article>
                        ))}
                      </div>
                    ) : (
                      <div className='mujian-no-active-models' role='status'>
                        <span className='mujian-no-active-icon'>
                          <Route size={18} aria-hidden='true' />
                        </span>
                        <div>
                          <strong>当前筛选下没有已开放模型</strong>
                          <p>
                            候选模型需要管理员完成渠道连接、模型同步和价格配置后才会开放。
                          </p>
                          <Button
                            size='small'
                            type='tertiary'
                            icon={<RefreshCw size={13} aria-hidden='true' />}
                            onClick={retryCatalog}
                          >
                            重新检查配置
                          </Button>
                        </div>
                      </div>
                    )}
                  </div>
                )}

                {(availability === 'all' || availability === 'pending') &&
                  filteredPendingItems.length > 0 && (
                    <div className='mujian-model-section pending'>
                      <header>
                        <div>
                          <span className='mujian-model-section-kicker'>
                            CATALOG
                          </span>
                          <h2>待开放目录</h2>
                        </div>
                        <strong>{filteredPendingItems.length}</strong>
                      </header>
                      <div className='mujian-pending-model-list'>
                        {filteredPendingItems.map((item) => (
                          <article
                            key={item.id}
                            className='mujian-pending-model-row'
                          >
                            <span className={`mujian-model-mark ${item.kind}`}>
                              {item.kind === 'chat' ? 'AI' : 'IMG'}
                            </span>
                            <div className='mujian-pending-model-name'>
                              <h3>{item.name}</h3>
                              <p>{item.provider_name}</p>
                            </div>
                            <div className='mujian-model-tags'>
                              {(item.tags || []).slice(0, 2).map((tag) => (
                                <Tag key={tag}>{tag}</Tag>
                              ))}
                            </div>
                            <div className='mujian-pending-reason'>
                              <Clock3 size={13} aria-hidden='true' />
                              {item.unavailable_reason || '等待渠道配置'}
                            </div>
                          </article>
                        ))}
                      </div>
                    </div>
                  )}
              </>
            ) : catalogItems.length ? (
              <div className='mujian-curated-empty' role='status'>
                <Search size={20} aria-hidden='true' />
                <strong>当前筛选没有匹配结果</strong>
                <span>这只代表筛选结果为空，不会改变模型的开放状态。</span>
                <Button type='tertiary' onClick={resetFilters}>
                  清除搜索与筛选
                </Button>
              </div>
            ) : (
              <div className='mujian-curated-empty' role='status'>
                <Route size={20} aria-hidden='true' />
                <strong>模型目录尚未完成配置</strong>
                <span>
                  管理员需要依次完成渠道连接、模型同步和价格配置；完成前这里不会显示可用模型。
                </span>
                <Button
                  type='primary'
                  icon={<RefreshCw size={14} aria-hidden='true' />}
                  onClick={retryCatalog}
                >
                  重新检查配置
                </Button>
              </div>
            )}
          </div>
        </section>
      ) : (
        <ModelPricingPage />
      )}
    </div>
  );
};

export default Pricing;
