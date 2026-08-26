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
  copy,
  showError,
  showNotice,
  getLogo,
  getSystemName,
} from '../../helpers';
import { useSearchParams, Link } from 'react-router-dom';
import { Button, Card, Form, Typography, Banner } from '@douyinfe/semi-ui';
import { IconMail, IconLock, IconCopy } from '@douyinfe/semi-icons';
import { useTranslation } from 'react-i18next';
import AuthShell from './AuthShell';

const { Text, Title } = Typography;

const PasswordResetConfirm = () => {
  const { t } = useTranslation();
  const [inputs, setInputs] = useState({
    email: '',
    token: '',
  });
  const { email, token } = inputs;
  const isValidResetLink = Boolean(email && token);
  const invalidLinkMessage = t('无效的重置链接，请重新发起密码重置请求');

  const [loading, setLoading] = useState(false);
  const [disableButton, setDisableButton] = useState(false);
  const [countdown, setCountdown] = useState(30);
  const [newPassword, setNewPassword] = useState('');
  const [searchParams] = useSearchParams();
  const [formApi, setFormApi] = useState(null);
  const [feedback, setFeedback] = useState(null);

  const logo = getLogo();
  const systemName = getSystemName();

  useEffect(() => {
    const token = searchParams.get('token');
    const email = searchParams.get('email');
    setInputs({
      token: token || '',
      email: email || '',
    });
    if (formApi) {
      formApi.setValues({
        email: email || '',
        newPassword: newPassword || '',
      });
    }
  }, [searchParams, newPassword, formApi]);

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
    if (!email || !token) {
      setFeedback({ type: 'danger', message: invalidLinkMessage });
      showError(invalidLinkMessage);
      return;
    }

    setDisableButton(true);
    setLoading(true);
    setFeedback(null);

    try {
      const res = await API.post(`/api/user/reset`, {
        email,
        token,
      });
      const { success, message } = res.data;
      if (success) {
        const password = res.data.data;
        setNewPassword(password);
        setFeedback({ type: 'success', message: t('密码重置完成') });
        await copy(password);
        showNotice(`${t('密码已重置并已复制到剪贴板：')} ${password}`);
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
    <AuthShell mode='reset-confirm' systemName={systemName}>
      <div className='flex flex-col items-center'>
        <div className='w-full max-w-md'>
          <div className='flex items-center justify-center mb-6 gap-2'>
            <img src={logo} alt={systemName} className='h-10 w-10 rounded-[9px] object-cover' />
            <Title heading={3}>{systemName}</Title>
          </div>

          <Card className='border-0 !rounded-2xl overflow-hidden'>
            <div className='flex justify-center pt-6 pb-2'>
              <Title id='password-reset-confirm-title' heading={3}>
                {t('密码重置确认')}
              </Title>
            </div>
            <div className='px-2 py-8'>
              {!isValidResetLink && (
                <div role='alert' aria-live='assertive' className='mb-4'>
                  <Banner
                    type='danger'
                    description={invalidLinkMessage}
                    closeIcon={null}
                  />
                </div>
              )}

              {feedback && isValidResetLink && (
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
                getFormApi={(api) => setFormApi(api)}
                initValues={{
                  email: email || '',
                  newPassword: newPassword || '',
                }}
                className='space-y-4'
                aria-labelledby='password-reset-confirm-title'
              >
                <Form.Input
                  field='email'
                  label={t('邮箱')}
                  name='email'
                  type='email'
                  autoComplete='email'
                  disabled={true}
                  prefix={<IconMail />}
                  placeholder={email ? '' : t('等待获取邮箱信息...')}
                />

                {newPassword && (
                  <Form.Input
                    field='newPassword'
                    label={t('新密码')}
                    name='newPassword'
                    autoComplete='new-password'
                    disabled={true}
                    prefix={<IconLock />}
                    suffix={
                      <Button
                        icon={<IconCopy />}
                        type='tertiary'
                        theme='borderless'
                        onClick={async () => {
                          await copy(newPassword);
                          showNotice(
                            `${t('密码已复制到剪贴板：')} ${newPassword}`,
                          );
                        }}
                      >
                        {t('复制')}
                      </Button>
                    }
                  />
                )}

                <div className='space-y-2 pt-2'>
                  <Button
                    theme='solid'
                    className='w-full !rounded-full'
                    type='primary'
                    htmlType='submit'
                    onClick={handleSubmit}
                    loading={loading}
                    disabled={
                      disableButton || Boolean(newPassword) || !isValidResetLink
                    }
                  >
                    {newPassword ? t('密码重置完成') : t('确认重置密码')}
                  </Button>
                </div>
              </Form>

              <div className='mt-6 text-center text-sm'>
                <Text>
                  <Link
                    to='/login'
                    className='text-[#ff6a00] hover:text-[#e85d00] font-medium'
                  >
                    {t('返回登录')}
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

export default PasswordResetConfirm;
