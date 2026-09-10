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

import React, { useState } from 'react';
import { Button, Input, Modal } from '@douyinfe/semi-ui';
import { API, showError, showSuccess } from '../../../helpers';

export default function YuYuPricingImport({ disabled, onImported }) {
  const [visible, setVisible] = useState(false);
  const [group, setGroup] = useState('auto');
  const [file, setFile] = useState(null);
  const [saving, setSaving] = useState(false);

  const importPricing = async () => {
    setSaving(true);
    try {
      if (file.size > 2 * 1024 * 1024) throw new Error('价格文件不能超过 2 MB');
      const pricing = JSON.parse(await file.text());
      const response = await API.put(
        '/api/mujian/admin/providers/yuyu/pricing',
        {
          upstream_group: group.trim(),
          pricing,
        },
      );
      if (!response.data.success) throw new Error(response.data.message);
      showSuccess('羽宇价格已导入，请同步模型并测试后启用');
      setVisible(false);
      setFile(null);
      await onImported();
    } catch (error) {
      showError(error.response?.data?.message || error.message || '导入失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Button disabled={disabled} onClick={() => setVisible(true)}>
        导入羽宇价格
      </Button>
      <Modal
        title='导入羽宇官方价格'
        visible={visible}
        onCancel={() => !saving && setVisible(false)}
        onOk={importPricing}
        confirmLoading={saving}
        okButtonProps={{ disabled: !file || !group.trim() || saving }}
        cancelButtonProps={{ disabled: saving }}
        okText='导入并暂停羽宇路由'
      >
        <p>
          登录羽宇并打开价格页，在浏览器开发者工具的 Network 中，将 /api/pricing
          的完整响应保存为 JSON 文件。
          导入会暂停羽宇路由，之后请同步模型、测试并重新启用。
        </p>
        <label>
          羽宇令牌分组（须与羽宇后台一致，自动分组填 auto）
          <Input
            aria-label='羽宇上游令牌分组'
            value={group}
            onChange={setGroup}
            disabled={saving}
          />
        </label>
        <p>
          <input
            type='file'
            accept='.json,application/json'
            aria-label='羽宇官方价格 JSON'
            disabled={saving}
            onChange={(event) => setFile(event.target.files?.[0] || null)}
          />
        </p>
        <p>
          系统仅开放当前计费规则可以准确结算的模型，未支持的规则会显示原因。换
          Key 或调整上游分组后需重新导入。
        </p>
      </Modal>
    </>
  );
}
