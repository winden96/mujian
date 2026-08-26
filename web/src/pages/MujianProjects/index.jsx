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
import { Link, useNavigate } from 'react-router-dom';
import {
  Button,
  Empty,
  Form,
  Modal,
  Progress,
  Spin,
  Tag,
} from '@douyinfe/semi-ui';
import { KeyRound, Plus, WandSparkles } from 'lucide-react';
import { API, showError, showSuccess } from '../../helpers';
import { MUJIAN_TOKEN_PATH } from '../../helpers/authReturn';
import '../mujian.css';

const updatedAtFormatter = new Intl.DateTimeFormat('zh-CN', {
  month: 'numeric',
  day: 'numeric',
});

const formatUpdatedAt = (timestamp) => {
  if (!timestamp) return '刚刚更新';
  return `${updatedAtFormatter.format(new Date(timestamp * 1000))} 更新`;
};

const ProjectCard = ({ project }) => {
  const [coverFailed, setCoverFailed] = useState(false);
  const showCover = Boolean(project.cover_url) && !coverFailed;
  const projectPath = `/console/mujian/projects/${project.id}/workspace`;

  return (
    <Link
      className='mujian-project-card'
      to={projectPath}
      aria-label={`打开项目：${project.title}`}
      onKeyDown={(event) => {
        if (event.key === ' ') {
          event.preventDefault();
          event.currentTarget.click();
        }
      }}
    >
      <article
        className={`mujian-project-card-inner${showCover ? ' has-cover' : ''}`}
      >
        {showCover && (
          <div className='mujian-project-cover'>
            <img
              src={project.cover_url}
              alt={`${project.title}项目封面`}
              onError={() => setCoverFailed(true)}
            />
          </div>
        )}
        <div className='mujian-project-body'>
          <div className='mujian-project-meta'>
            <Tag color='orange'>{project.type || '短剧'}</Tag>
            <span>{formatUpdatedAt(project.updated_at)}</span>
          </div>
          <h3>{project.title}</h3>
          <p>{project.synopsis || '等待补充故事梗概'}</p>
          <div className='mujian-project-footer'>
            <div className='mujian-project-progress'>
              <span>第 {project.current_episode} 集</span>
              <span>{project.progress}%</span>
            </div>
            <Progress
              aria-label={`${project.title}创作进度`}
              percent={project.progress}
              showInfo={false}
              stroke='var(--semi-color-primary)'
              size='small'
            />
          </div>
        </div>
      </article>
    </Link>
  );
};

const MujianProjects = () => {
  const navigate = useNavigate();
  const [projects, setProjects] = useState([]);
  const [loading, setLoading] = useState(true);
  const [visible, setVisible] = useState(false);
  const [saving, setSaving] = useState(false);
  const [formApi, setFormApi] = useState(null);

  const loadProjects = async () => {
    setLoading(true);
    try {
      const response = await API.get('/api/mujian/projects');
      if (!response.data.success) throw new Error(response.data.message);
      setProjects(response.data.data || []);
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || '项目加载失败',
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    loadProjects();
  }, []);

  const createProject = async () => {
    try {
      const values = await formApi.validate();
      setSaving(true);
      const response = await API.post('/api/mujian/projects', values);
      if (!response.data.success) throw new Error(response.data.message);
      showSuccess('项目已创建');
      setVisible(false);
      navigate(`/console/mujian/projects/${response.data.data.id}/workspace`);
    } catch (error) {
      if (error?.errors) return;
      showError(error.response?.data?.message || error.message || '创建失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className='mujian-page'>
      <section className='mujian-page-header'>
        <div>
          <div className='mujian-eyebrow'>
            <WandSparkles size={14} /> AI 短剧创作工作台
          </div>
          <h1>项目</h1>
          <p>从剧本、分镜到画面，在同一条创作链路里持续推进。</p>
        </div>
        <div className='mujian-page-header-actions'>
          <Button
            icon={<KeyRound size={16} />}
            onClick={() => navigate(MUJIAN_TOKEN_PATH)}
          >
            API Key
          </Button>
          <Button
            theme='solid'
            type='primary'
            icon={<Plus size={16} />}
            onClick={() => setVisible(true)}
          >
            新建项目
          </Button>
        </div>
      </section>

      <Spin spinning={loading}>
        {projects.length === 0 && !loading ? (
          <Empty description='还没有项目，先创建一个故事。' />
        ) : (
          <section className='mujian-project-grid'>
            {projects.map((project) => (
              <ProjectCard key={project.id} project={project} />
            ))}
          </section>
        )}
      </Spin>

      <Modal
        title='新建短剧项目'
        visible={visible}
        onCancel={() => setVisible(false)}
        onOk={createProject}
        confirmLoading={saving}
        okText='创建并进入'
      >
        <Form getFormApi={setFormApi} labelPosition='top'>
          <Form.Input
            field='title'
            label='项目名称'
            rules={[{ required: true, message: '请输入项目名称' }]}
            placeholder='例如：雨夜便利店'
          />
          <Form.TextArea
            field='synopsis'
            label='故事梗概'
            rows={4}
            placeholder='用几句话说清人物、冲突和悬念。'
          />
        </Form>
      </Modal>
    </main>
  );
};

export default MujianProjects;
