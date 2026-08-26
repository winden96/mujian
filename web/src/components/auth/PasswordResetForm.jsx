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

import React, { useEffect, useState } from 'react';
import {
  API,
  getLogo,
  showError,
  showInfo,
  showSuccess,
  getSystemName,
} from '../../helpers';
import Turnstile from 'react-turnstile';
import { Banner, Button, Card, Form, Typography } from '@douyinfe/semi-ui';
import { IconMail } from '@douyinfe/semi-icons';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import AuthShell from './AuthShell';

const { Text, Title } = Typography;

const PasswordResetForm = () => {
  const { t } = useTranslation();
  const [email, setEmail] = useState('');

  const [loading, setLoading] = useState(false);
  const [turnstileEnabled, setTurnstileEnabled] = useState(false);
  const [turnstileSiteKey, setTurnstileSiteKey] = useState('');
  const [turnstileToken, setTurnstileToken] = useState('');
  const [disableButton, setDisableButton] = useState(false);
  const [countdown, setCountdown] = useState(30);
  const [feedback, setFeedback] = useState(null);

  const logo = getLogo();
  const systemName = getSystemName();

  useEffect(() => {
    const storedStatus = localStorage.getItem('status');
    if (!storedStatus) return;

    try {
      const status = JSON.parse(storedStatus);
      if (!status.turnstile_check) return;

      setTurnstileEnabled(true);
      setTurnstileSiteKey(status.turnstile_site_key);
    } catch {
      // A stale status cache must not prevent account recovery.
    }
  }, []);

  useEffect(() => {
    let countdownInterval = null;
    if (disableButton && countdown > 0) {
      countdownInterval = setInterval(() => {
        setCountdown(countdown - 1);
      }, 1000);
    } else if (countdown === 0) {
      setDisableButton(false);
      setCountdown(30);
    }
    return () => clearInterval(countdownInterval);
  }, [disableButton, countdown]);

  async function handleSubmit() {
    if (!email) {
      const message = t('请输入邮箱地址');
      setFeedback({ type: 'danger', message });
      showError(message);
      return;
    }
    if (turnstileEnabled && turnstileToken === '') {
      const message = t('请稍后几秒重试，Turnstile 正在检查用户环境！');
      setFeedback({ type: 'warning', message });
      showInfo(message);
      return;
    }

    setDisableButton(true);
    setLoading(true);
    setFeedback(null);

    try {
      const res = await API.get(
        `/api/reset_password?email=${email}&turnstile=${turnstileToken}`,
      );
      const { success, message } = res.data;
      if (success) {
        const successMessage = t('重置邮件发送成功，请检查邮箱！');
        setFeedback({ type: 'success', message: successMessage });
        showSuccess(successMessage);
        setEmail('');
      } else {
        setFeedback({ type: 'danger', message });
        showError(message);
      }
    } catch {
      const message = t('网络连接失败，请检查网络设置或稍后重试');
      setFeedback({ type: 'danger', message });
      showError(message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <AuthShell
      mode='reset'
      systemName={systemName}
      footer={
        turnstileEnabled && (
          <Turnstile sitekey={turnstileSiteKey} onVerify={setTurnstileToken} />
        )
      }
    >
      <div className='flex flex-col items-center'>
        <div className='w-full max-w-md'>
          <div className='flex items-center justify-center mb-6 gap-2'>
            <img src={logo} alt={systemName} className='h-10 w-10 rounded-[9px] object-cover' />
            <Title heading={3}>{systemName}</Title>
          </div>

          <Card className='border-0 !rounded-2xl overflow-hidden'>
            <div className='flex justify-center pt-6 pb-2'>
              <Title id='password-reset-title' heading={3}>
                {t('密码重置')}
              </Title>
            </div>
            <div className='px-2 py-8'>
              {feedback && (
                <div
                  role={feedback.type === 'danger' ? 'alert' : 'status'}
                  aria-live='polite'
                  className='mb-4'
                >
                  <Banner
                    type={feedback.type}
                    description={feedback.message}
                    closeIcon={null}
                  />
                </div>
              )}

              <Form
                className='space-y-3'
                aria-labelledby='password-reset-title'
              >
                <Form.Input
                  field='email'
                  label={t('邮箱')}
                  placeholder={t('请输入您的邮箱地址')}
                  name='email'
                  type='email'
                  autoComplete='email'
                  value={email}
                  onChange={setEmail}
                  prefix={<IconMail />}
                  aria-required='true'
                />

                <div className='space-y-2 pt-2'>
                  <Button
                    theme='solid'
                    className='w-full !rounded-full'
                    type='primary'
                    htmlType='submit'
                    onClick={handleSubmit}
                    loading={loading}
                    disabled={disableButton}
                  >
                    {disableButton ? `${t('重试')} (${countdown})` : t('提交')}
                  </Button>
                </div>
              </Form>

              <div className='mt-6 text-center text-sm'>
                <Text>
                  {t('想起来了？')}{' '}
                  <Link
                    to='/login'
                    className='text-[#ff6a00] hover:text-[#e85d00] font-medium'
                  >
                    {t('登录')}
                  </Link>
                </Text>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </AuthShell>
  );
};

export default PasswordResetForm;
