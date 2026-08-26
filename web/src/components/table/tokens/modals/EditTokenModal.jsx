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

import React, { useEffect, useState, useRef } from 'react';
import { API, showError, showSuccess } from '../../../../helpers';
import { useIsMobile } from '../../../../hooks/common/useIsMobile';
import { Modal, Spin, Form } from '@douyinfe/semi-ui';
import { useTranslation } from 'react-i18next';

const EditTokenModal = (props) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const isMobile = useIsMobile();
  const formApiRef = useRef(null);
  const isEdit = props.editingToken.id !== undefined;

  const getInitValues = () => ({
    name: '',
    enabled: true,
  });

  const handleCancel = () => {
    props.handleClose();
  };

  const loadToken = async () => {
    setLoading(true);
    let res = await API.get(`/api/token/${props.editingToken.id}`);
    const { success, message, data } = res.data;
    if (success) {
      formApiRef.current?.setValues({
        name: data.name || '',
        enabled: data.status === 1,
      });
    } else {
      showError(message);
    }
    setLoading(false);
  };

  useEffect(() => {
    if (props.visiable) {
      if (isEdit) {
        loadToken();
      } else {
        formApiRef.current?.setValues(getInitValues());
      }
    } else {
      formApiRef.current?.reset();
    }
  }, [props.visiable, props.editingToken.id]);

  const buildPayload = (values) => ({
    name: (values.name || '').trim(),
    status: values.enabled ? 1 : 2,
    expired_time: -1,
    unlimited_quota: true,
    remain_quota: 0,
    model_limits_enabled: false,
    model_limits: '',
    allow_ips: '',
    group: '',
    cross_group_retry: false,
  });

  const submit = async (values) => {
    const payload = buildPayload(values);
    if (!payload.name) {
      showError(t('请输入名称'));
      return;
    }
    setLoading(true);
    try {
      if (isEdit) {
        const res = await API.put(`/api/token/`, {
          ...payload,
          id: parseInt(props.editingToken.id, 10),
        });
        const { success, message } = res.data;
        if (!success) {
          showError(t(message));
          return;
        }
        showSuccess(t('令牌更新成功！'));
      } else {
        const res = await API.post(`/api/token/`, payload);
        const { success, message } = res.data;
        if (!success) {
          showError(t(message));
          return;
        }
        showSuccess(t('令牌创建成功，请在列表页面点击复制获取令牌！'));
      }
      props.refresh();
      props.handleClose();
      formApiRef.current?.setValues(getInitValues());
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      title={isEdit ? t('更新令牌信息') : t('创建新的令牌')}
      visible={props.visiable}
      onCancel={handleCancel}
      onOk={() => formApiRef.current?.submitForm()}
      confirmLoading={loading}
      okText={t('提交')}
      cancelText={t('取消')}
      width={isMobile ? '90%' : 420}
      centered
    >
      <Spin spinning={loading}>
        <Form
          key={isEdit ? 'edit' : 'new'}
          initValues={getInitValues()}
          getFormApi={(api) => (formApiRef.current = api)}
          onSubmit={submit}
        >
          {() => (
            <>
              <Form.Input
                field='name'
                label={t('密钥名称')}
                placeholder={t('请输入名称')}
                rules={[{ required: true, message: t('请输入名称') }]}
                autoComplete='off'
                inputProps={{
                  autoComplete: 'off',
                  name: 'token-key-name',
                }}
              />
              <Form.Switch
                field='enabled'
                label={t('启用')}
                extraText={t('关闭后此密钥立即失效，可随时重新打开')}
              />
            </>
          )}
        </Form>
      </Spin>
    </Modal>
  );
};

export default EditTokenModal;
