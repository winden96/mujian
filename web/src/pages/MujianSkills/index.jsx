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
import { Button, Card, Switch, Tag } from '@douyinfe/semi-ui';
import { Clapperboard, Image, MessageSquareText } from 'lucide-react';
import { API, showError, showSuccess } from '../../helpers';
import '../mujian.css';

const catalog = [
  {
    name: '短剧编剧',
    icon: MessageSquareText,
    color: 'violet',
    detail: '人物动机、冲突节奏、对白润色与连续性检查。',
  },
  {
    name: '分镜导演',
    icon: Clapperboard,
    color: 'orange',
    detail: '将场景拆成景别、机位、时长和镜头动作。',
  },
  {
    name: '画面提示词',
    icon: Image,
    color: 'cyan',
    detail: '把分镜转成适配图像模型的可生成提示词。',
  },
];

const MujianSkills = () => {
  const [enabled, setEnabled] = useState([]);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    API.get('/api/mujian/skills')
      .then((response) => setEnabled(response.data.data || []))
      .catch(() => showError('Skills 加载失败'));
  }, []);
  const toggle = (name) =>
    setEnabled((items) =>
      items.includes(name)
        ? items.filter((item) => item !== name)
        : [...items, name],
    );
  const save = async () => {
    setSaving(true);
    try {
      await API.put('/api/mujian/skills', { skills: enabled });
      showSuccess('Skills 已保存');
    } catch (error) {
      showError(error.response?.data?.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };
  return (
    <main className='mujian-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>Agent 能力</div>
          <h1>Skills</h1>
          <p>选择 Agent 在项目中可以调用的专业能力。</p>
        </div>
        <Button theme='solid' loading={saving} onClick={save}>
          保存设置
        </Button>
      </section>
      <section className='mujian-skill-grid'>
        {catalog.map(({ name, icon: Icon, color, detail }) => (
          <Card key={name} className='mujian-skill-card'>
            <div className={`mujian-skill-icon ${color}`}>
              <Icon size={20} />
            </div>
            <div>
              <h3>{name}</h3>
              <p>{detail}</p>
              <Tag color={color}>内置 Skill</Tag>
            </div>
            <Switch
              checked={enabled.includes(name)}
              onChange={() => toggle(name)}
            />
          </Card>
        ))}
      </section>
    </main>
  );
};

export default MujianSkills;
