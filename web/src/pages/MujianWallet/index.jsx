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
import { Button, Card, InputNumber, Spin, Tag } from '@douyinfe/semi-ui';
import { Coins, Info } from 'lucide-react';
import { API, showError, showSuccess } from '../../helpers';
import '../mujian.css';

const formatCredits = (value) => {
  if (value < 0.01) return '<0.01';
  if (Number.isInteger(value)) return String(value);
  return value.toFixed(2);
};

const MujianWallet = () => {
  const [wallet, setWallet] = useState(null);
  const [credits, setCredits] = useState(100);
  const [loading, setLoading] = useState(true);
  const load = () =>
    API.get('/api/mujian/wallet')
      .then((response) => setWallet(response.data.data))
      .catch(() => showError('钱包加载失败'))
      .finally(() => setLoading(false));
  useEffect(load, []);
  const recharge = async () => {
    try {
      const response = await API.post('/api/mujian/wallet/demo-recharge', {
        credits,
      });
      setWallet(response.data.data);
      showSuccess('演示充值成功，不发起真实支付');
    } catch (error) {
      showError(error.response?.data?.message || '当前环境未开启演示充值');
    }
  };
  return (
    <main className='mujian-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>
            <Coins size={14} /> NewAPI 统一额度
          </div>
          <h1>积分钱包</h1>
          <p>文本与图像调用统一由 NewAPI 计费、结算和记录。</p>
        </div>
      </section>
      <Spin spinning={loading}>
        <section className='mujian-wallet-grid'>
          <Card className='mujian-balance-card'>
            <Tag color='violet'>可用余额</Tag>
            <strong>
              {formatCredits(wallet?.credits || 0)} <small>积分</small>
            </strong>
            <p>10 积分 = ¥1 · 1 USD = 73 积分</p>
            <div className='mujian-quota-note'>
              底层 NewAPI quota：{wallet?.quota?.toLocaleString() || 0}
            </div>
          </Card>
          <Card title='演示充值'>
            <div className='mujian-demo-notice'>
              <Info size={16} />
              <span>仅用于本地原型验证，不发起真实支付。</span>
            </div>
            <InputNumber
              value={credits}
              min={1}
              max={10000}
              onChange={setCredits}
              suffix='积分'
            />
            <Button theme='solid' onClick={recharge}>
              演示充值
            </Button>
          </Card>
        </section>
      </Spin>
    </main>
  );
};

export default MujianWallet;
