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

import { useEffect } from 'react';
import { Link } from 'react-router-dom';
import { getLogo } from '../../helpers';
import './auth-shell.css';

const authCopy = {
  login: {
    eyebrow: 'SEEDANCE 视频制作服务',
    title: '把一个需求，做成一支视频。',
    description:
      '从脚本、分镜到连续镜头，幕间把 Seedance 的视频生成能力放进一个可协作、可交付的制作流程。',
    marker: 'SEEDANCE · PRODUCTION READY',
    entryLabel: '登录幕间 AI',
    entryEyebrow: 'SEEDANCE 视频制作入口',
    entryDescription: '登录后继续制作你的项目',
  },
  register: {
    eyebrow: 'SEEDANCE 视频制作服务',
    title: '从一个需求，开始做视频。',
    description:
      '告别只有提示词、没有成片的断层。把你的想法交给幕间，沿着脚本、分镜和镜头完成视频制作。',
    marker: 'SEEDANCE · START A PROJECT',
    entryLabel: '注册幕间 AI',
    entryEyebrow: 'SEEDANCE 视频制作入口',
    entryDescription: '注册后创建第一个视频项目',
  },
  reset: {
    eyebrow: '幕间 AI 账户恢复',
    title: '回到你的制作线。',
    description:
      '输入绑定邮箱，我们会向你发送安全的密码重置链接，不会影响已有项目。',
    marker: 'SEEDANCE · ACCOUNT RECOVERY',
    entryLabel: '重置幕间 AI 登录密码',
    entryEyebrow: '账户恢复',
    entryDescription: '验证绑定邮箱，获取一次性重置链接',
  },
  'reset-confirm': {
    eyebrow: '幕间 AI 安全验证',
    title: '确认链接，继续创作。',
    description: '确认邮箱与一次性凭证后，幕间会生成新密码并复制到剪贴板。',
    marker: 'SEEDANCE · SECURE RESET',
    entryLabel: '确认幕间 AI 密码重置',
    entryEyebrow: '安全验证',
    entryDescription: '使用邮件中的一次性凭证完成密码重置',
  },
};

const creationSteps = ['梳理视频需求', '生成脚本与分镜', '制作可交付视频'];

const AuthShell = ({ mode, systemName, children, footer }) => {
  const copy = authCopy[mode];
  const logo = getLogo();

  useEffect(() => {
    document.body.classList.add('mujian-auth-route');
    return () => document.body.classList.remove('mujian-auth-route');
  }, []);

  return (
    <main className={`mujian-auth mujian-auth--${mode}`}>
      <div className='mujian-auth-stars' aria-hidden='true' />
      <div
        className='mujian-auth-glow mujian-auth-glow--blue'
        aria-hidden='true'
      />
      <div
        className='mujian-auth-glow mujian-auth-glow--pink'
        aria-hidden='true'
      />
      <div
        className='mujian-auth-orbit mujian-auth-orbit--one'
        aria-hidden='true'
      />
      <div
        className='mujian-auth-orbit mujian-auth-orbit--two'
        aria-hidden='true'
      />

      <section
        className='mujian-auth-story'
        aria-labelledby='mujian-auth-title'
      >
        <Link
          className='mujian-auth-story-brand'
          to='/'
          aria-label='返回幕间 AI 首页'
        >
          <img src={logo} alt='' className='mujian-auth-story-mark' />
          <span>{systemName}</span>
        </Link>

        <div className='mujian-auth-story-copy'>
          <span className='mujian-auth-eyebrow'>{copy.eyebrow}</span>
          <h1 id='mujian-auth-title'>{copy.title}</h1>
          <p>{copy.description}</p>
        </div>

        <div
          className='mujian-auth-story-steps'
          aria-label='Seedance 视频制作流程'
        >
          {creationSteps.map((step, index) => (
            <div key={step}>
              <b>{String(index + 1).padStart(2, '0')}</b>
              <span>{step}</span>
            </div>
          ))}
        </div>

        <span className='mujian-auth-timecode'>{copy.marker}</span>
      </section>

      <section className='mujian-auth-entry' aria-label={copy.entryLabel}>
        <div className='mujian-auth-entry-heading'>
          <span>{copy.entryEyebrow}</span>
          <p>{copy.entryDescription}</p>
        </div>
        <div className='mujian-auth-entry-content'>{children}</div>
        {footer && <div className='mujian-auth-entry-footer'>{footer}</div>}
      </section>
    </main>
  );
};

export default AuthShell;
