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

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Button,
  Empty,
  InputNumber,
  Modal,
  Spin,
  Tag,
} from '@douyinfe/semi-ui';
import {
  ArrowRight,
  CheckCircle2,
  Clock3,
  Coins,
  CreditCard,
  History,
  RefreshCw,
  Settings2,
  ShieldCheck,
  WalletCards,
  XCircle,
} from 'lucide-react';
import { SiAlipay, SiWechat } from 'react-icons/si';
import { QRCodeSVG } from 'qrcode.react';
import { useNavigate } from 'react-router-dom';
import { API, showError, showInfo, showSuccess } from '../../helpers';
import { isRoot } from '../../helpers/utils';
import '../mujian.css';

const formatCredits = (value) => {
  if (value < 0.01) return '<0.01';
  if (Number.isInteger(value)) return String(value);
  return value.toFixed(2);
};

const ORDER_STATUS = {
  pending: { label: '待支付', color: 'orange', icon: Clock3 },
  success: { label: '已到账', color: 'green', icon: CheckCircle2 },
  failed: { label: '支付失败', color: 'red', icon: XCircle },
  expired: { label: '已失效', color: 'grey', icon: XCircle },
};

const formatDate = (timestamp) => {
  if (!timestamp) return '—';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(timestamp * 1000));
};

const submitCheckout = ({ method, url, fields }) => {
  const form = document.createElement('form');
  form.method = method || 'POST';
  form.action = url;
  Object.entries(fields || {}).forEach(([name, value]) => {
    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = name;
    input.value = value;
    form.appendChild(input);
  });
  document.body.appendChild(form);
  form.submit();
  form.remove();
};

const MujianWallet = () => {
  const navigate = useNavigate();
  const [wallet, setWallet] = useState(null);
  const [credits, setCredits] = useState(100);
  const [paymentMethod, setPaymentMethod] = useState('alipay');
  const [orders, setOrders] = useState([]);
  const [loading, setLoading] = useState(true);
  const [ordersLoading, setOrdersLoading] = useState(true);
  const [paying, setPaying] = useState(false);
  const [qrCheckout, setQrCheckout] = useState(null);

  const loadWallet = useCallback(async () => {
    try {
      const response = await API.get('/api/mujian/wallet');
      const data = response.data.data;
      setWallet(data);
      if (data.payment?.methods?.length) {
        setPaymentMethod((current) =>
          data.payment.methods.some((method) => method.type === current)
            ? current
            : data.payment.methods[0].type,
        );
      }
    } catch {
      showError('钱包加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  const loadOrders = useCallback(async () => {
    setOrdersLoading(true);
    try {
      const response = await API.get(
        '/api/mujian/wallet/orders?p=1&page_size=6',
      );
      setOrders(response.data.data?.items || []);
    } catch {
      showError('充值记录加载失败');
    } finally {
      setOrdersLoading(false);
    }
  }, []);

  const refreshQrOrder = useCallback(async () => {
    if (!qrCheckout) return;
    try {
      const response = await API.get(
        '/api/mujian/wallet/orders?p=1&page_size=6',
      );
      const nextOrders = response.data.data?.items || [];
      setOrders(nextOrders);
      const currentOrder = nextOrders.find(
        (order) => order.trade_no === qrCheckout.order.trade_no,
      );
      if (currentOrder?.status === 'success') {
        setQrCheckout(null);
        await loadWallet();
        showSuccess('微信支付成功，积分已到账');
      } else if (
        currentOrder?.status === 'failed' ||
        currentOrder?.status === 'expired'
      ) {
        setQrCheckout(null);
        showError('该支付订单已失效，请重新下单');
      }
    } catch {
      // 保持二维码可用，用户可手动刷新订单状态。
    }
  }, [loadWallet, qrCheckout]);

  useEffect(() => {
    Promise.all([loadWallet(), loadOrders()]);
    const returnedFromPayment =
      new URLSearchParams(window.location.search).get('payment') === 'return';
    if (returnedFromPayment) {
      showInfo('支付结果确认中，到账状态以订单记录为准');
      const timer = window.setTimeout(() => {
        loadWallet();
        loadOrders();
      }, 1800);
      return () => window.clearTimeout(timer);
    }
    return undefined;
  }, [loadOrders, loadWallet]);

  useEffect(() => {
    if (!qrCheckout) return undefined;
    const timer = window.setInterval(refreshQrOrder, 2000);
    return () => window.clearInterval(timer);
  }, [qrCheckout, refreshQrOrder]);

  const paymentAmount = useMemo(() => {
    const rate = wallet?.credits_per_cny || 10;
    return Number(credits || 0) / rate;
  }, [credits, wallet]);

  const creditsValid = useMemo(() => {
    if (!wallet) return false;
    const custom = wallet.custom_amount;
    return (
      Number.isInteger(credits) &&
      credits >= custom.min &&
      credits <= custom.max &&
      credits % custom.step === 0
    );
  }, [credits, wallet]);

  const createPayment = async () => {
    if (!creditsValid) {
      showError('请输入 10 到 10000 之间、且为 10 倍数的积分');
      return;
    }
    setPaying(true);
    try {
      const response = await API.post('/api/mujian/wallet/pay', {
        credits,
        payment_method: paymentMethod,
      });
      const payment = response.data.data;
      if (payment.checkout.method === 'QR_CODE') {
        setQrCheckout(payment);
        await loadOrders();
      } else {
        submitCheckout(payment.checkout);
      }
    } catch (error) {
      showError(error.response?.data?.message || '支付请求失败');
    } finally {
      setPaying(false);
    }
  };

  const renderPaymentIcon = (method, size = 20) =>
    method === 'admin' ? (
      <CreditCard size={size} aria-hidden='true' />
    ) : method === 'alipay' ? (
      <SiAlipay size={size} aria-hidden='true' />
    ) : (
      <SiWechat size={size} aria-hidden='true' />
    );

  return (
    <main className='mujian-page mujian-wallet-page'>
      <section className='mujian-page-header mujian-wallet-header'>
        <div>
          <div className='mujian-eyebrow'>
            <WalletCards size={14} /> 账户与计费
          </div>
          <h1>积分钱包</h1>
          <p>充值、余额与账单，一处清晰管理。</p>
        </div>
        <Button
          theme='borderless'
          icon={<History size={17} />}
          onClick={() =>
            document
              .querySelector('#wallet-orders')
              ?.scrollIntoView({ behavior: 'smooth' })
          }
        >
          充值记录
        </Button>
      </section>
      <Spin spinning={loading}>
        <div className='mujian-wallet-shell'>
          <section className='mujian-wallet-overview' aria-label='钱包余额'>
            <div className='mujian-balance-copy'>
              <span className='mujian-wallet-label'>可用积分</span>
              <div className='mujian-wallet-balance'>
                <strong>{wallet ? formatCredits(wallet.credits) : '—'}</strong>
                <span>积分</span>
              </div>
              <p>用于剧本、分镜与画面生成，调用后实时扣减。</p>
            </div>
            <div className='mujian-wallet-facts'>
              <div>
                <Coins size={18} />
                <span>积分价值</span>
                <strong>{wallet?.credits_per_cny || 10} 积分 = ¥1</strong>
              </div>
              <div>
                <ShieldCheck size={18} />
                <span>支付保障</span>
                <strong>签名验证 · 异步入账</strong>
              </div>
            </div>
          </section>

          <section
            className='mujian-wallet-recharge'
            aria-labelledby='wallet-recharge-title'
          >
            <div className='mujian-wallet-section-heading'>
              <div>
                <span className='mujian-wallet-kicker'>充值积分</span>
                <h2 id='wallet-recharge-title'>选择本次充值额度</h2>
              </div>
              <span className='mujian-wallet-rate'>价格固定，无隐藏费用</span>
            </div>

            <div
              className='mujian-credit-packages'
              role='group'
              aria-label='快捷积分套餐'
            >
              {(wallet?.packages || []).map((item) => (
                <button
                  key={item.credits}
                  type='button'
                  className={`mujian-credit-package ${credits === item.credits ? 'is-selected' : ''}`}
                  aria-pressed={credits === item.credits}
                  onClick={() => setCredits(item.credits)}
                >
                  <span>{item.credits.toLocaleString()} 积分</span>
                  <strong>¥{item.amount.toFixed(2)}</strong>
                </button>
              ))}
            </div>

            <div className='mujian-custom-credit-row'>
              <label htmlFor='wallet-credit-input'>自定义积分</label>
              <div>
                <InputNumber
                  id='wallet-credit-input'
                  value={credits}
                  min={wallet?.custom_amount?.min || 10}
                  max={wallet?.custom_amount?.max || 10000}
                  step={wallet?.custom_amount?.step || 10}
                  precision={0}
                  onChange={(value) => setCredits(Number(value || 0))}
                  suffix='积分'
                />
                <span>须为 10 的倍数，单次最高 10,000 积分</span>
              </div>
            </div>

            {wallet?.payment?.enabled ? (
              <div className='mujian-payment-panel'>
                <div
                  className='mujian-payment-methods'
                  role='radiogroup'
                  aria-label='支付方式'
                >
                  {wallet.payment.methods.map((method) => (
                    <button
                      key={method.type}
                      type='button'
                      role='radio'
                      aria-checked={paymentMethod === method.type}
                      className={`mujian-payment-method ${method.type} ${paymentMethod === method.type ? 'is-selected' : ''}`}
                      onClick={() => setPaymentMethod(method.type)}
                    >
                      {renderPaymentIcon(method.type)}
                      <span>{method.name}</span>
                      <i aria-hidden='true' />
                    </button>
                  ))}
                </div>
                <div className='mujian-payment-summary'>
                  <div>
                    <span>本次获得</span>
                    <strong>
                      {Number(credits || 0).toLocaleString()} 积分
                    </strong>
                  </div>
                  <div>
                    <span>应付金额</span>
                    <strong>¥{paymentAmount.toFixed(2)}</strong>
                  </div>
                  <Button
                    theme='solid'
                    size='large'
                    loading={paying}
                    disabled={!creditsValid}
                    icon={<CreditCard size={18} />}
                    onClick={createPayment}
                  >
                    确认支付
                  </Button>
                </div>
              </div>
            ) : (
              <div className='mujian-payment-disabled' role='status'>
                <div className='mujian-payment-disabled-icon'>
                  <CreditCard size={21} />
                </div>
                <div>
                  <strong>在线支付暂未开放</strong>
                  <span>
                    {wallet?.payment?.disabled_reason || '支付渠道尚未配置'}
                  </span>
                </div>
                {isRoot() && (
                  <Button
                    theme='light'
                    icon={<Settings2 size={16} />}
                    onClick={() => navigate('/console/setting?tab=payment')}
                  >
                    前往支付设置
                  </Button>
                )}
              </div>
            )}
          </section>

          <section
            className='mujian-wallet-orders'
            id='wallet-orders'
            aria-labelledby='wallet-orders-title'
          >
            <div className='mujian-wallet-section-heading'>
              <div>
                <span className='mujian-wallet-kicker'>最近账单</span>
                <h2 id='wallet-orders-title'>充值记录</h2>
              </div>
              <Button
                theme='borderless'
                icon={<RefreshCw size={16} />}
                loading={ordersLoading}
                onClick={() => {
                  loadWallet();
                  loadOrders();
                }}
              >
                刷新
              </Button>
            </div>
            <Spin spinning={ordersLoading}>
              {orders.length ? (
                <div className='mujian-order-list'>
                  {orders.map((order) => {
                    const status =
                      ORDER_STATUS[order.status] || ORDER_STATUS.pending;
                    const StatusIcon = status.icon;
                    return (
                      <article className='mujian-order-row' key={order.id}>
                        <div
                          className={`mujian-order-method ${order.payment_method}`}
                        >
                          {renderPaymentIcon(order.payment_method, 18)}
                        </div>
                        <div className='mujian-order-primary'>
                          <strong>{order.credits.toLocaleString()} 积分</strong>
                          <span>{order.trade_no}</span>
                        </div>
                        <div className='mujian-order-money'>
                          <strong>
                            {order.payment_method === 'admin'
                              ? '管理员充值'
                              : `¥${order.money.toFixed(2)}`}
                          </strong>
                          <span>{formatDate(order.create_time)}</span>
                        </div>
                        <Tag
                          color={status.color}
                          prefixIcon={<StatusIcon size={13} />}
                        >
                          {status.label}
                        </Tag>
                      </article>
                    );
                  })}
                </div>
              ) : (
                <Empty
                  className='mujian-wallet-empty'
                  title='暂无充值记录'
                  description='完成首次充值后，订单会显示在这里。'
                />
              )}
            </Spin>
          </section>

          <div className='mujian-wallet-footnote'>
            <ShieldCheck size={15} />
            <span>
              支付结果由服务端签名回调确认。如付款后未立即到账，请刷新订单状态。
            </span>
            <ArrowRight size={14} />
          </div>
        </div>
      </Spin>
      <Modal
        visible={Boolean(qrCheckout)}
        title='微信扫码支付'
        width={390}
        centered
        maskClosable={false}
        onCancel={() => setQrCheckout(null)}
        footer={
          <div className='mujian-wechatpay-modal-actions'>
            <Button theme='borderless' onClick={() => setQrCheckout(null)}>
              稍后支付
            </Button>
            <Button theme='solid' onClick={refreshQrOrder}>
              我已支付，刷新状态
            </Button>
          </div>
        }
      >
        {qrCheckout && (
          <div className='mujian-wechatpay-modal'>
            <div className='mujian-wechatpay-qr'>
              <QRCodeSVG
                value={qrCheckout.checkout.code_url}
                size={220}
                level='M'
                title='微信支付二维码'
              />
            </div>
            <strong>请使用微信“扫一扫”</strong>
            <span>
              应付 ¥{qrCheckout.order.money.toFixed(2)} · 到账{' '}
              {qrCheckout.order.credits.toLocaleString()} 积分
            </span>
            <small>支付后将自动刷新，请勿重复下单。</small>
          </div>
        )}
      </Modal>
    </main>
  );
};

export default MujianWallet;
