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

import React, { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import {
  Button,
  Card,
  Input,
  Select,
  Spin,
  Tag,
  TextArea,
} from '@douyinfe/semi-ui';
import {
  Check,
  ChevronRight,
  Image as ImageIcon,
  Send,
  Sparkles,
} from 'lucide-react';
import { API, showError, showSuccess } from '../../helpers';
import '../mujian.css';

const stages = ['剧本创作', '拆分分镜', '生成画面'];

const parseDialogues = (value) => {
  try {
    return JSON.parse(value || '[]');
  } catch {
    return [];
  }
};

const MujianWorkspace = () => {
  const { projectId } = useParams();
  const [workspace, setWorkspace] = useState(null);
  const [preference, setPreference] = useState(null);
  const [models, setModels] = useState({ chat: [], image: [] });
  const [stage, setStage] = useState(1);
  const [message, setMessage] = useState('');
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(false);
  const [generating, setGenerating] = useState('');

  const load = async () => {
    setLoading(true);
    try {
      const [workspaceResponse, preferenceResponse] = await Promise.all([
        API.get(`/api/mujian/projects/${projectId}/workspace`),
        API.get('/api/mujian/preferences'),
      ]);
      if (!workspaceResponse.data.success)
        throw new Error(workspaceResponse.data.message);
      setWorkspace(workspaceResponse.data.data);
      setPreference(preferenceResponse.data.data);
      setModels(preferenceResponse.data.models || { chat: [], image: [] });
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || '工作区加载失败',
      );
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, [projectId]);

  const currentScene = workspace?.scenes?.[0];
  const projectMessages = useMemo(() => workspace?.messages || [], [workspace]);

  const updateDefault = async (field, value) => {
    try {
      const response = await API.put('/api/mujian/preferences', {
        [field]: value,
      });
      if (!response.data.success) throw new Error(response.data.message);
      setPreference(response.data.data);
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || '模型切换失败',
      );
    }
  };

  const sendMessage = async () => {
    if (!message.trim()) return;
    setSending(true);
    try {
      const response = await API.post(
        `/api/mujian/projects/${projectId}/agent/messages`,
        {
          content: message,
          skill:
            stage === 1 ? '短剧编剧' : stage === 2 ? '分镜导演' : '画面提示词',
          model_id: preference.default_chat_model,
        },
      );
      if (!response.data.success) throw new Error(response.data.message);
      setMessage('');
      await load();
    } catch (error) {
      showError(
        error.response?.data?.message || error.message || 'Agent 调用失败',
      );
    } finally {
      setSending(false);
    }
  };

  const applyProposal = async (messageId) => {
    try {
      const response = await API.post(
        `/api/mujian/projects/${projectId}/agent/messages/${messageId}/apply`,
      );
      if (!response.data.success) throw new Error(response.data.message);
      setWorkspace(response.data.data);
      showSuccess('提案已应用到项目');
    } catch (error) {
      showError(error.response?.data?.message || error.message || '应用失败');
    }
  };

  const generateShot = async (shotId) => {
    setGenerating(shotId);
    try {
      const response = await API.post(
        `/api/mujian/projects/${projectId}/shots/${shotId}/generate`,
        { model_id: preference.default_image_model },
      );
      if (!response.data.success) throw new Error(response.data.message);
      setWorkspace((current) => ({
        ...current,
        shots: current.shots.map((shot) =>
          shot.id === shotId ? response.data.data.shot : shot,
        ),
      }));
      showSuccess('画面已生成');
    } catch (error) {
      showError(error.response?.data?.message || error.message || '生成失败');
    } finally {
      setGenerating('');
    }
  };

  return (
    <Spin spinning={loading}>
      <main className='mujian-workspace'>
        <aside className='mujian-stage-rail'>
          {stages.map((title, index) => (
            <button
              type='button'
              key={title}
              className={stage === index + 1 ? 'active' : ''}
              onClick={() => setStage(index + 1)}
            >
              <span>{String(index + 1).padStart(2, '0')}</span>
              <strong>{title}</strong>
            </button>
          ))}
        </aside>

        <section className='mujian-workspace-main'>
          <header className='mujian-workspace-heading'>
            <div>
              <Tag color='violet'>
                第 {workspace?.project?.current_episode || 1} 集
              </Tag>
              <h1>{workspace?.project?.title || '创作工作区'}</h1>
            </div>
            <div className='mujian-model-switches'>
              <Select
                value={preference?.default_chat_model}
                onChange={(value) => updateDefault('default_chat_model', value)}
                optionList={models.chat.map((value) => ({
                  label: value,
                  value,
                }))}
              />
              <Select
                value={preference?.default_image_model}
                onChange={(value) =>
                  updateDefault('default_image_model', value)
                }
                optionList={models.image.map((value) => ({
                  label: value,
                  value,
                }))}
              />
            </div>
          </header>

          {stage === 1 && (
            <Card className='mujian-script-card'>
              <div className='mujian-section-title'>
                <span>
                  场景{' '}
                  {String(currentScene?.scene_number || 1).padStart(2, '0')}
                </span>
                <Tag>版本 {currentScene?.version || 1}</Tag>
              </div>
              <h2>{currentScene?.title}</h2>
              <p className='environment'>{currentScene?.environment}</p>
              <p>{currentScene?.action}</p>
              <div className='mujian-dialogues'>
                {parseDialogues(currentScene?.dialogues).map((item, index) => (
                  <div key={`${item.character}-${index}`}>
                    <strong>{item.character}</strong>
                    <span>{item.line}</span>
                  </div>
                ))}
              </div>
            </Card>
          )}

          {stage === 2 && (
            <div className='mujian-shot-list'>
              {(workspace?.shots || []).map((shot) => (
                <Card key={shot.id} className='mujian-shot-row'>
                  <div className='mujian-shot-index'>
                    {String(shot.sequence).padStart(2, '0')}
                  </div>
                  <div>
                    <strong>{shot.shot_type}</strong>
                    <p>{shot.prompt}</p>
                  </div>
                  <Tag>{shot.duration_seconds} 秒</Tag>
                  <ChevronRight size={18} />
                </Card>
              ))}
            </div>
          )}

          {stage === 3 && (
            <div className='mujian-image-grid'>
              {(workspace?.shots || []).map((shot) => (
                <Card
                  key={shot.id}
                  className='mujian-image-card'
                  bodyStyle={{ padding: 0 }}
                >
                  <div className='mujian-image-preview'>
                    {shot.result_url ? (
                      <img
                        src={shot.result_url}
                        alt={`${shot.shot_type}生成画面`}
                      />
                    ) : (
                      <ImageIcon size={30} />
                    )}
                  </div>
                  <div className='mujian-image-meta'>
                    <div>
                      <strong>
                        {String(shot.sequence).padStart(2, '0')} ·{' '}
                        {shot.shot_type}
                      </strong>
                      <Tag
                        color={shot.status === 'completed' ? 'green' : 'grey'}
                      >
                        {shot.status === 'completed' ? '已完成' : '待生成'}
                      </Tag>
                    </div>
                    <p>{shot.prompt}</p>
                    <Button
                      block
                      theme='solid'
                      loading={generating === shot.id}
                      onClick={() => generateShot(shot.id)}
                    >
                      生成画面
                    </Button>
                  </div>
                </Card>
              ))}
            </div>
          )}
        </section>

        <aside className='mujian-agent-panel'>
          <div className='mujian-agent-title'>
            <span>
              <Sparkles size={18} />
            </span>
            <div>
              <strong>创作 Agent</strong>
              <small>{preference?.default_chat_model}</small>
            </div>
          </div>
          <div className='mujian-agent-thread'>
            {projectMessages.length === 0 && (
              <div className='mujian-agent-empty'>
                告诉 Agent 你想如何调整当前内容。
              </div>
            )}
            {projectMessages.map((item) => (
              <div
                key={item.id}
                className={`mujian-agent-message ${item.role}`}
              >
                <span>{item.content}</span>
                {item.apply_status === 'pending' && (
                  <Button
                    size='small'
                    icon={<Check size={14} />}
                    onClick={() => applyProposal(item.id)}
                  >
                    应用提案
                  </Button>
                )}
              </div>
            ))}
          </div>
          <div className='mujian-agent-composer'>
            <TextArea
              value={message}
              onChange={setMessage}
              autosize={{ minRows: 3, maxRows: 6 }}
              placeholder='描述你想修改的内容…'
            />
            <Button
              theme='solid'
              icon={<Send size={15} />}
              loading={sending}
              onClick={sendMessage}
            >
              发送
            </Button>
          </div>
        </aside>
      </main>
    </Spin>
  );
};

export default MujianWorkspace;
