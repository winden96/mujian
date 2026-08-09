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

import React, { useContext, useEffect, useState } from 'react';
import { Button, Select } from '@douyinfe/semi-ui';
import ModelPricingPage from '../../components/table/model-pricing/layout/PricingPage';
import { UserContext } from '../../context/User';
import { API, isAdmin, showError, showSuccess } from '../../helpers';
import '../mujian.css';

const Pricing = () => {
  const [userState] = useContext(UserContext);
  const [preference, setPreference] = useState(null);
  const [models, setModels] = useState({ chat: [], image: [] });

  const showDefaults = Boolean(userState?.user) && !isAdmin();

  useEffect(() => {
    if (!showDefaults) return;
    API.get('/api/mujian/preferences')
      .then((response) => {
        if (!response.data.success) throw new Error(response.data.message);
        setPreference(response.data.data);
        setModels(response.data.models || { chat: [], image: [] });
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

  return (
    <div className='mujian-pricing-page'>
      {showDefaults && (
        <section className='mujian-pricing-defaults' aria-label='默认创作模型'>
          <div>
            <strong>默认创作模型</strong>
            <span>只展示管理员已开通渠道并完成价格配置的模型</span>
          </div>
          <label>
            对话模型
            <Select
              value={preference?.default_chat_model}
              optionList={options(models.chat)}
              disabled={!models.chat.length}
              onChange={(value) => updateDefault('default_chat_model', value)}
            />
          </label>
          <label>
            图像模型
            <Select
              value={preference?.default_image_model}
              optionList={options(models.image)}
              disabled={!models.image.length}
              onChange={(value) => updateDefault('default_image_model', value)}
            />
          </label>
          {!models.chat.length && !models.image.length && (
            <Button disabled>等待管理员配置渠道与价格</Button>
          )}
        </section>
      )}
      <ModelPricingPage />
    </div>
  );
};

export default Pricing;
