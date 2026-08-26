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

import React from 'react';
import {
  Button,
  Space,
  SplitButtonGroup,
  Tag,
  AvatarGroup,
  Avatar,
  Tooltip,
  Progress,
  Popover,
  Typography,
  Input,
  Modal,
} from '@douyinfe/semi-ui';
import {
  timestamp2string,
  renderGroup,
  renderQuota,
  getModelCategories,
  showError,
} from '../../../helpers';
import {
  IconTreeTriangleDown,
  IconCopy,
  IconEyeOpened,
  IconEyeClosed,
} from '@douyinfe/semi-icons';

// progress color helper
const getProgressColor = (pct) => {
  if (pct === 100) return 'var(--semi-color-success)';
  if (pct <= 10) return 'var(--semi-color-danger)';
  if (pct <= 30) return 'var(--semi-color-warning)';
  return undefined;
};

// Render functions
function renderTimestamp(timestamp) {
  return <>{timestamp2string(timestamp)}</>;
}

// Render status column only (no usage)
const renderStatus = (text, record, t) => {
  const enabled = text === 1;

  let tagColor = 'black';
  let tagText = t('未知状态');
  if (enabled) {
    tagColor = 'green';
    tagText = t('已启用');
  } else if (text === 2) {
    tagColor = 'red';
    tagText = t('已禁用');
  } else if (text === 3) {
    tagColor = 'yellow';
    tagText = t('已过期');
  } else if (text === 4) {
    tagColor = 'grey';
    tagText = t('已耗尽');
  }

  return (
    <Tag color={tagColor} shape='circle' size='small'>
      {tagText}
    </Tag>
  );
};

// Render group column
const renderGroupColumn = (text, record, t, groupRatios = {}) => {
  if (text === 'auto') {
    return (
      <Tooltip
        content={t(
          '当前分组为 auto，会自动选择最优分组，当一个组不可用时自动降级到下一个组（熔断机制）',
        )}
        position='top'
      >
        <Tag color='white' shape='circle'>
          {t('智能熔断')}
          {record && record.cross_group_retry ? `(${t('跨分组')})` : ''}
        </Tag>
      </Tooltip>
    );
  }
  const ratio = groupRatios[text];
  return (
    <span className='flex items-center gap-1'>
      {renderGroup(text)}
      {ratio !== undefined && (
        <Tag size='small' color='green' shape='circle'>
          {ratio}x
        </Tag>
      )}
    </span>
  );
};

// Render token key column with show/hide and copy functionality
const renderTokenKey = (
  text,
  record,
  showKeys,
  resolvedTokenKeys,
  loadingTokenKeys,
  toggleTokenVisibility,
  copyTokenKey,
  copyTokenConnectionString,
  t,
) => {
  const revealed = !!showKeys[record.id];
  const loading = !!loadingTokenKeys[record.id];
  const fullKey = resolvedTokenKeys[record.id];
  const displayedKey =
    revealed && fullKey
      ? `sk-${fullKey}`
      : record.key
        ? `sk-${record.key}`
        : '';

  return (
    <div className='w-[220px]'>
      <Input
        readOnly
        mode={revealed ? undefined : 'password'}
        value={displayedKey}
        size='small'
        suffix={
          <div className='flex items-center'>
            <Button
              theme='borderless'
              size='small'
              type='tertiary'
              icon={revealed ? <IconEyeClosed /> : <IconEyeOpened />}
              loading={loading}
              aria-label={revealed ? t('隐藏密钥') : t('显示密钥')}
              onClick={async (e) => {
                e.stopPropagation();
                await toggleTokenVisibility(record);
              }}
            />
            <Button
              theme='borderless'
              size='small'
              type='tertiary'
              icon={<IconCopy />}
              loading={loading}
              aria-label={t('复制密钥')}
              onClick={async (e) => {
                e.stopPropagation();
                await copyTokenKey(record);
              }}
            />
          </div>
        }
      />
    </div>
  );
};

// Render model limits column
const renderModelLimits = (text, record, t) => {
  if (record.model_limits_enabled && text) {
    const models = text.split(',').filter(Boolean);
    const categories = getModelCategories(t);

    const vendorAvatars = [];
    const matchedModels = new Set();
    Object.entries(categories).forEach(([key, category]) => {
      if (key === 'all') return;
      if (!category.icon || !category.filter) return;
      const vendorModels = models.filter((m) =>
        category.filter({ model_name: m }),
      );
      if (vendorModels.length > 0) {
        vendorAvatars.push(
          <Tooltip
            key={key}
            content={vendorModels.join(', ')}
            position='top'
            showArrow
          >
            <Avatar
              size='extra-extra-small'
              alt={category.label}
              color='transparent'
            >
              {category.icon}
            </Avatar>
          </Tooltip>,
        );
        vendorModels.forEach((m) => matchedModels.add(m));
      }
    });

    const unmatchedModels = models.filter((m) => !matchedModels.has(m));
    if (unmatchedModels.length > 0) {
      vendorAvatars.push(
        <Tooltip
          key='unknown'
          content={unmatchedModels.join(', ')}
          position='top'
          showArrow
        >
          <Avatar size='extra-extra-small' alt='unknown'>
            {t('其他')}
          </Avatar>
        </Tooltip>,
      );
    }

    return <AvatarGroup size='extra-extra-small'>{vendorAvatars}</AvatarGroup>;
  } else {
    return (
      <Tag color='white' shape='circle'>
        {t('无限制')}
      </Tag>
    );
  }
};

// Render IP restrictions column
const renderAllowIps = (text, t) => {
  if (!text || text.trim() === '') {
    return (
      <Tag color='white' shape='circle'>
        {t('无限制')}
      </Tag>
    );
  }

  const ips = text
    .split('\n')
    .map((ip) => ip.trim())
    .filter(Boolean);

  const displayIps = ips.slice(0, 1);
  const extraCount = ips.length - displayIps.length;

  const ipTags = displayIps.map((ip, idx) => (
    <Tag key={idx} shape='circle'>
      {ip}
    </Tag>
  ));

  if (extraCount > 0) {
    ipTags.push(
      <Tooltip
        key='extra'
        content={ips.slice(1).join(', ')}
        position='top'
        showArrow
      >
        <Tag shape='circle'>{'+' + extraCount}</Tag>
      </Tooltip>,
    );
  }

  return <Space wrap>{ipTags}</Space>;
};

// Render separate quota usage column
const renderQuotaUsage = (text, record, t) => {
  const { Paragraph } = Typography;
  const used = parseInt(record.used_quota) || 0;
  const remain = parseInt(record.remain_quota) || 0;
  const total = used + remain;
  if (record.unlimited_quota) {
    const popoverContent = (
      <div className='text-xs p-2'>
        <Paragraph copyable={{ content: renderQuota(used) }}>
          {t('已用额度')}: {renderQuota(used)}
        </Paragraph>
      </div>
    );
    return (
      <Popover content={popoverContent} position='top'>
        <Tag color='white' shape='circle'>
          {t('无限额度')}
        </Tag>
      </Popover>
    );
  }
  const percent = total > 0 ? (remain / total) * 100 : 0;
  const popoverContent = (
    <div className='text-xs p-2'>
      <Paragraph copyable={{ content: renderQuota(used) }}>
        {t('已用额度')}: {renderQuota(used)}
      </Paragraph>
      <Paragraph copyable={{ content: renderQuota(remain) }}>
        {t('剩余额度')}: {renderQuota(remain)} ({percent.toFixed(0)}%)
      </Paragraph>
      <Paragraph copyable={{ content: renderQuota(total) }}>
        {t('总额度')}: {renderQuota(total)}
      </Paragraph>
    </div>
  );
  return (
    <Popover content={popoverContent} position='top'>
      <Tag color='white' shape='circle'>
        <div className='flex flex-col items-end'>
          <span className='text-xs leading-none'>{`${renderQuota(remain)} / ${renderQuota(total)}`}</span>
          <Progress
            percent={percent}
            stroke={getProgressColor(percent)}
            aria-label='quota usage'
            format={() => `${percent.toFixed(0)}%`}
            style={{ width: '100%', marginTop: '1px', marginBottom: 0 }}
          />
        </div>
      </Tag>
    </Popover>
  );
};

// Render operations column
const renderOperations = (
  text,
  record,
  onOpenLink,
  setEditingToken,
  setShowEdit,
  manageToken,
  refresh,
  t,
) => {
  return (
    <Space wrap>
      {record.status === 1 ? (
        <Button
          type='danger'
          size='small'
          onClick={async () => {
            await manageToken(record.id, 'disable', record);
            await refresh();
          }}
        >
          {t('禁用')}
        </Button>
      ) : (
        <Button
          size='small'
          onClick={async () => {
            await manageToken(record.id, 'enable', record);
            await refresh();
          }}
        >
          {t('启用')}
        </Button>
      )}

      <Button
        type='tertiary'
        size='small'
        onClick={() => {
          setEditingToken(record);
          setShowEdit(true);
        }}
      >
        {t('编辑')}
      </Button>

      <Button
        type='danger'
        size='small'
        onClick={() => {
          Modal.confirm({
            title: t('确定是否要删除此令牌？'),
            content: t('此修改将不可逆'),
            onOk: () => {
              (async () => {
                await manageToken(record.id, 'delete', record);
                await refresh();
              })();
            },
          });
        }}
      >
        {t('删除')}
      </Button>
    </Space>
  );
};

export const getTokensColumns = ({
  t,
  showKeys,
  resolvedTokenKeys,
  loadingTokenKeys,
  toggleTokenVisibility,
  copyTokenKey,
  copyTokenConnectionString,
  manageToken,
  onOpenLink,
  setEditingToken,
  setShowEdit,
  refresh,
  groupRatios = {},
}) => {
  return [
    {
      title: t('名称'),
      dataIndex: 'name',
    },
    {
      title: t('密钥'),
      key: 'token_key',
      render: (text, record) =>
        renderTokenKey(
          text,
          record,
          showKeys,
          resolvedTokenKeys,
          loadingTokenKeys,
          toggleTokenVisibility,
          copyTokenKey,
          copyTokenConnectionString,
          t,
        ),
    },
    {
      title: t('状态'),
      dataIndex: 'status',
      key: 'status',
      render: (text, record) => renderStatus(text, record, t),
    },
    {
      title: '',
      dataIndex: 'operate',
      fixed: 'right',
      render: (text, record, index) =>
        renderOperations(
          text,
          record,
          onOpenLink,
          setEditingToken,
          setShowEdit,
          manageToken,
          refresh,
          t,
        ),
    },
  ];
};
