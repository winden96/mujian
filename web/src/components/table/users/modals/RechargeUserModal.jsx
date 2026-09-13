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

import React, { useEffect, useRef, useState } from 'react';
import {
  Banner,
  Button,
  InputNumber,
  Modal,
  TextArea,
} from '@douyinfe/semi-ui';
import {
  API,
  renderMujianCreditsFromQuota,
  showSuccess,
} from '../../../../helpers';

export default function RechargeUserModal({
  user,
  adminId,
  onCancel,
  onSuccess,
  t,
}) {
  const storageKey = `admin-recharge:${adminId}:${user.id}`;
  const [pending, setPending] = useState(() => {
    const saved = sessionStorage.getItem(storageKey);
    return saved ? JSON.parse(saved) : null;
  });
  const [credits, setCredits] = useState(pending?.credits ?? 100);
  const [remark, setRemark] = useState(pending?.remark ?? '');
  const [quota, setQuota] = useState(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const submitting = useRef(false);
  const valid =
    Number.isInteger(credits) &&
    credits >= 1 &&
    credits <= 1000000 &&
    Array.from(remark).length <= 200;

  useEffect(() => {
    const abort = new AbortController();
    API.get(`/api/user/${user.id}`, {
      signal: abort.signal,
      skipErrorHandler: true,
    })
      .then(({ data }) => {
        if (!data.success) throw new Error(data.message);
        setQuota(data.data.quota);
      })
      .catch((err) => {
        if (!abort.signal.aborted)
          setError(err.response?.data?.message || err.message);
      });
    return () => abort.abort();
  }, [user.id]);

  const submit = async () => {
    if (submitting.current || !valid || quota === null) return;
    submitting.current = true;
    setLoading(true);
    setError('');
    try {
      const request = pending || {
        credits,
        remark,
        request_id: crypto.randomUUID(),
      };
      // Keep an uncertain request through closing/reopening or refreshing the page.
      sessionStorage.setItem(storageKey, JSON.stringify(request));
      setPending(request);
      const { data } = await API.post(
        `/api/user/${user.id}/recharge`,
        request,
        { skipErrorHandler: true },
      );
      if (!data.success) throw new Error(data.message || t('充值失败'));
      sessionStorage.removeItem(storageKey);
      showSuccess(
        `${t('充值成功')} · ${user.username} +${request.credits.toLocaleString()} ${t('积分')}`,
      );
      onSuccess();
    } catch (err) {
      const status = err.response?.status;
      if (status >= 400 && status < 500 && status !== 408) {
        sessionStorage.removeItem(storageKey);
        setPending(null);
      }
      setError(
        err.response?.data?.message || err.message || t('充值失败，请重试'),
      );
    } finally {
      submitting.current = false;
      setLoading(false);
    }
  };

  return (
    <Modal
      visible
      title={t('直接充值')}
      onCancel={() => {
        if (!submitting.current) onCancel();
      }}
      maskClosable={false}
      width={520}
      style={{ maxWidth: 'calc(100vw - 32px)' }}
      footer={
        <Button
          theme='solid'
          loading={loading}
          disabled={!valid || quota === null || loading}
          onClick={submit}
          style={{
            width: '100%',
            height: 'auto',
            minHeight: 40,
            padding: '8px 12px',
          }}
        >
          <span
            style={{
              whiteSpace: 'normal',
              overflowWrap: 'anywhere',
              lineHeight: 1.5,
            }}
          >
            {pending ? t('重试原充值') : t('确认充值')} · {user.username} +
            {Number(credits || 0).toLocaleString()} {t('积分')}
          </span>
        </Button>
      }
    >
      <div className='flex flex-col gap-4'>
        <div>
          <strong>{user.username}</strong> · ID {user.id}
        </div>
        <div>
          {t('当前积分')}：
          {quota === null ? t('加载中…') : renderMujianCreditsFromQuota(quota)}
        </div>
        <label htmlFor='admin-recharge-credits'>
          {t('充值积分（1～1,000,000）')}
        </label>
        <InputNumber
          id='admin-recharge-credits'
          aria-label={t('充值积分')}
          value={credits}
          onChange={setCredits}
          min={1}
          max={1000000}
          step={1}
          disabled={Boolean(pending) || loading}
          style={{ width: '100%' }}
        />
        <label htmlFor='admin-recharge-remark'>
          {t('充值备注（选填，最多 200 字）')}
        </label>
        <TextArea
          id='admin-recharge-remark'
          aria-label={t('充值备注')}
          value={remark}
          onChange={setRemark}
          disabled={Boolean(pending) || loading}
        />
        {Array.from(remark).length > 200 && (
          <Banner type='danger' description={t('备注最多 200 字')} />
        )}
        <span className='text-sm text-gray-500'>
          {t('确认后立即增加该账号积分，实付金额为 0，并生成管理员充值记录。')}
        </span>
        {pending && (
          <Banner
            type='info'
            description={t(
              '该笔充值结果尚待确认；重试将核对原订单，不会重复到账。',
            )}
          />
        )}
        {error && <Banner type='danger' description={error} />}
      </div>
    </Modal>
  );
}
