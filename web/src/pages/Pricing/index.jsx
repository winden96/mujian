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
import { Input, Select, Tag } from '@douyinfe/semi-ui';
import {
  CircleCheck,
  Clock3,
  Image as ImageIcon,
  MessageSquareText,
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

const Pricing = () => {
  const [userState] = useContext(UserContext);
  const [preference, setPreference] = useState(null);
  const [models, setModels] = useState(EMPTY_MODELS);
  const [search, setSearch] = useState('');
  const [kind, setKind] = useState('all');
  const [availability, setAvailability] = useState('all');

  const showDefaults = Boolean(userState?.user) && !isAdmin();

  useEffect(() => {
    if (!showDefaults) return;
    API.get('/api/mujian/preferences')
      .then((response) => {
        if (!response.data.success) throw new Error(response.data.message);
        setPreference(response.data.data);
        setModels({ ...EMPTY_MODELS, ...response.data.models });
      })
      .catch((error) =>
        showError(error.response?.data?.message || error.message),
      );
  }, [showDefaults]);

  const updateDefault = async (field, value) => {
    try {
      const response = await API.put('/api/mujian/preferences', {
        [field]: value,
      });
      if (!response.data.success) throw new Error(response.data.message);
      setPreference(response.data.data);
      showSuccess('默认模型已更新');
    } catch (error) {
      showError(error.response?.data?.message || error.message || '更新失败');
    }
  };

  const options = (values) => values.map((value) => ({ label: value, value }));
  const chatModelAvailable = models.chat.includes(
    preference?.default_chat_model,
  );
  const imageModelAvailable = models.image.includes(
    preference?.default_image_model,
  );
  const catalogItems = models.items;
  const availableItems = catalogItems.filter((item) => item.available);
  const availableRouteCount = availableItems.reduce(
    (total, item) => total + item.channel_count,
    0,
  );

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

  const priceText = (item) => {
    if (item.billing_type === 'fixed') {
      const range =
        item.min_fixed_price === item.max_fixed_price
          ? `$${item.min_fixed_price.toFixed(3)}`
          : `$${item.min_fixed_price.toFixed(3)}–$${item.max_fixed_price.toFixed(3)}`;
      return `${range} / 次`;
    }
    const input =
      item.min_input_price === item.max_input_price
        ? `$${item.min_input_price.toFixed(2)}`
        : `$${item.min_input_price.toFixed(2)}–$${item.max_input_price.toFixed(2)}`;
    const output =
      item.min_output_price === item.max_output_price
        ? `$${item.min_output_price.toFixed(2)}`
        : `$${item.min_output_price.toFixed(2)}–$${item.max_output_price.toFixed(2)}`;
    return `输入 ${input} · 输出 ${output} / 1M`;
  };

  return (
    <div className='mujian-pricing-page'>
      {showDefaults && (
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
            <div className='mujian-market-stats' aria-label='模型广场状态'>
              <span>
                <CircleCheck size={14} aria-hidden='true' />
                <strong>{availableItems.length}</strong> 个可用模型
              </span>
              <span>
                <Route size={14} aria-hidden='true' />
                <strong>{availableRouteCount}</strong> 条可用路由
              </span>
              <span>{catalogItems.length} 个精选候选</span>
            </div>
          </div>
          <div className='mujian-pricing-defaults' aria-label='默认创作模型'>
            <header>
              <div>
                <strong>默认创作模型</strong>
                <span>工作台将自动使用这里的选择</span>
              </div>
              <span
                className={`mujian-default-status${models.chat.length || models.image.length ? ' ready' : ''}`}
              >
                {models.chat.length || models.image.length
                  ? '已连接'
                  : '等待渠道配置'}
              </span>
            </header>
            <div className='mujian-default-model-fields'>
              <label>
                <span>
                  <MessageSquareText size={14} aria-hidden='true' /> 对话模型
                </span>
                <Select
                  aria-label='默认对话模型'
                  value={
                    chatModelAvailable
                      ? preference?.default_chat_model
                      : undefined
                  }
                  placeholder={
                    models.chat.length ? '请选择对话模型' : '暂无可用模型'
                  }
                  optionList={options(models.chat)}
                  disabled={!models.chat.length}
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
                  value={
                    imageModelAvailable
                      ? preference?.default_image_model
                      : undefined
                  }
                  placeholder={
                    models.image.length ? '请选择图像模型' : '暂无可用模型'
                  }
                  optionList={options(models.image)}
                  disabled={!models.image.length}
                  onChange={(value) =>
                    updateDefault('default_image_model', value)
                  }
                />
              </label>
            </div>
          </div>
        </section>
      )}
      {showDefaults ? (
        <section className='mujian-curated-models' aria-label='精选模型目录'>
          <div className='mujian-model-toolbar'>
            <Input
              aria-label='搜索模型'
              prefix={<Search size={16} aria-hidden='true' />}
              placeholder='搜索模型名称、厂商或能力标签'
              value={search}
              showClear
              onChange={setSearch}
            />
            <div
              className='mujian-model-filter-group'
              aria-label='模型类型筛选'
            >
              <SlidersHorizontal size={15} aria-hidden='true' />
              {MODEL_KIND_FILTERS.map(([value, label]) => (
                <button
                  key={value}
                  type='button'
                  className={kind === value ? 'active' : ''}
                  aria-pressed={kind === value}
                  onClick={() => setKind(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            <div
              className='mujian-model-filter-group availability'
              aria-label='可用状态筛选'
            >
              {MODEL_STATUS_FILTERS.map(([value, label]) => (
                <button
                  key={value}
                  type='button'
                  className={availability === value ? 'active' : ''}
                  aria-pressed={availability === value}
                  onClick={() => setAvailability(value)}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>

          {filteredItems.length ? (
            <>
              {(availability === 'all' || availability === 'available') && (
                <div className='mujian-model-section'>
                  <header>
                    <div>
                      <span className='mujian-model-section-kicker'>READY</span>
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
                            <span className={`mujian-model-mark ${item.kind}`}>
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
                    <div className='mujian-no-active-models'>
                      <span className='mujian-no-active-icon'>
                        <Route size={18} aria-hidden='true' />
                      </span>
                      <div>
                        <strong>当前还没有可用模型</strong>
                        <p>
                          管理员完成渠道连接、模型同步和价格配置后，会自动开放到这里。
                        </p>
                      </div>
                    </div>
                  )}
                </div>
              )}

              {(availability === 'all' || availability === 'pending') && (
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
            <div className='mujian-curated-empty'>
              <Search size={20} aria-hidden='true' />
              <strong>没有匹配的模型</strong>
              <span>换个关键词或筛选条件试试。</span>
            </div>
          ) : (
            <div className='mujian-curated-empty'>
              <Route size={20} aria-hidden='true' />
              <strong>模型目录尚未同步</strong>
              <span>管理员同步供应商后，精选模型会自动出现在这里。</span>
            </div>
          )}
        </section>
      ) : (
        <ModelPricingPage />
      )}
    </div>
  );
};

export default Pricing;
