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
import { Banner, Button, Form, Spin } from '@douyinfe/semi-ui';
import {
  API,
  removeTrailingSlash,
  showError,
  showSuccess,
} from '../../../helpers';

const EMPTY_SETTINGS = {
  WechatPayEnabled: false,
  WechatPayAppID: '',
  WechatPayMchID: '',
  WechatPayMchCertificateSerialNumber: '',
  WechatPayAPIv3Key: '',
  WechatPayMerchantPrivateKeyPath: '',
  WechatPayPublicKeyID: '',
  WechatPayPublicKeyPath: '',
};

const REQUIRED_VISIBLE_FIELDS = [
  'WechatPayAppID',
  'WechatPayMchID',
  'WechatPayMchCertificateSerialNumber',
  'WechatPayMerchantPrivateKeyPath',
  'WechatPayPublicKeyID',
  'WechatPayPublicKeyPath',
];

export default function SettingsPaymentGatewayWechat({ options, refresh }) {
  const [inputs, setInputs] = useState(EMPTY_SETTINGS);
  const [loading, setLoading] = useState(false);
  const formApiRef = useRef(null);

  useEffect(() => {
    if (!options || !formApiRef.current) return;
    const next = {
      ...EMPTY_SETTINGS,
      WechatPayEnabled:
        options.WechatPayEnabled === true ||
        options.WechatPayEnabled === 'true',
      WechatPayAppID: options.WechatPayAppID || '',
      WechatPayMchID: options.WechatPayMchID || '',
      WechatPayMchCertificateSerialNumber:
        options.WechatPayMchCertificateSerialNumber || '',
      WechatPayMerchantPrivateKeyPath:
        options.WechatPayMerchantPrivateKeyPath || '',
      WechatPayPublicKeyID: options.WechatPayPublicKeyID || '',
      WechatPayPublicKeyPath: options.WechatPayPublicKeyPath || '',
    };
    setInputs(next);
    formApiRef.current.setValues(next);
  }, [options]);

  const save = async () => {
    if (inputs.WechatPayEnabled) {
      if (!String(options?.ServerAddress || '').startsWith('https://')) {
        showError('请先把服务器地址配置为公网 HTTPS 域名');
        return;
      }
      if (
        REQUIRED_VISIBLE_FIELDS.some((field) => !String(inputs[field]).trim())
      ) {
        showError('启用前请完整填写微信支付商户参数与密钥文件路径');
        return;
      }
      const hasSavedAPIKey =
        options?.WechatPayAPIv3KeyConfigured === true ||
        options?.WechatPayAPIv3KeyConfigured === 'true';
      if (!inputs.WechatPayAPIv3Key && !hasSavedAPIKey) {
        showError('首次启用时请填写 API v3 密钥');
        return;
      }
    }

    setLoading(true);
    try {
      const disableResponse = await API.put('/api/option/', {
        key: 'WechatPayEnabled',
        value: 'false',
      });
      if (!disableResponse.data.success) {
        throw new Error(disableResponse.data.message);
      }

      const settings = REQUIRED_VISIBLE_FIELDS.map((key) => ({
        key,
        value: String(inputs[key] || '').trim(),
      }));
      if (inputs.WechatPayAPIv3Key) {
        settings.push({
          key: 'WechatPayAPIv3Key',
          value: inputs.WechatPayAPIv3Key,
        });
      }
      for (const setting of settings) {
        const response = await API.put('/api/option/', setting);
        if (!response.data.success) throw new Error(response.data.message);
      }
      const enableResponse = await API.put('/api/option/', {
        key: 'WechatPayEnabled',
        value: inputs.WechatPayEnabled ? 'true' : 'false',
      });
      if (!enableResponse.data.success) {
        throw new Error(enableResponse.data.message);
      }
      showSuccess('微信支付设置已更新');
      setInputs((current) => ({ ...current, WechatPayAPIv3Key: '' }));
      await refresh?.();
    } catch (error) {
      showError(
        error.response?.data?.message ||
          error.message ||
          '微信支付设置更新失败',
      );
    } finally {
      setLoading(false);
    }
  };

  const callbackAddress = `${removeTrailingSlash(options?.ServerAddress || '') || '网站地址'}/api/mujian/wallet/wechatpay/notify`;

  return (
    <Spin spinning={loading}>
      <Form
        initValues={inputs}
        onValueChange={setInputs}
        getFormApi={(api) => (formApiRef.current = api)}
      >
        <Form.Section text='微信支付官方直连（API v3 / Native）'>
          <Banner
            type='info'
            description={`商户平台的支付通知 URL 使用：${callbackAddress}`}
          />
          <Banner
            type='warning'
            description='密钥文件路径是服务器上的绝对路径。请只授权给服务进程读取，不要放入 Web 目录或 Git 仓库。'
          />
          <Form.Switch
            field='WechatPayEnabled'
            label='启用微信支付'
            extraText='参数不完整时，钱包不会展示该支付方式'
          />
          <Form.Input field='WechatPayAppID' label='公众号 / 小程序 AppID' />
          <Form.Input field='WechatPayMchID' label='微信支付商户号' />
          <Form.Input
            field='WechatPayMchCertificateSerialNumber'
            label='商户 API 证书序列号'
          />
          <Form.Input
            field='WechatPayAPIv3Key'
            label='API v3 密钥'
            type='password'
            placeholder='留空表示保留已有密钥'
            extraText='必须为 32 字节 ASCII 字符，只用于解密微信回调'
          />
          <Form.Input
            field='WechatPayMerchantPrivateKeyPath'
            label='商户 API 私钥文件路径'
            placeholder='/run/secrets/wechatpay/apiclient_key.pem'
          />
          <Form.Input
            field='WechatPayPublicKeyID'
            label='微信支付公钥 ID'
            placeholder='PUB_KEY_ID_...'
          />
          <Form.Input
            field='WechatPayPublicKeyPath'
            label='微信支付公钥文件路径'
            placeholder='/run/secrets/wechatpay/wechatpay_public_key.pem'
          />
          <Button onClick={save}>保存微信支付设置</Button>
        </Form.Section>
      </Form>
    </Spin>
  );
}
