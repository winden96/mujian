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

import React from 'react';
import { Link } from 'react-router-dom';
import {
  ArrowRight,
  Check,
  Image,
  MessageSquareText,
  Sparkles,
  WandSparkles,
} from 'lucide-react';
import './mujian-home.css';

const shots = [
  { index: '01', title: '全景 · 雨夜便利店', seconds: '4 秒' },
  { index: '02', title: '中景 · 林夏推门', seconds: '3 秒' },
  { index: '03', title: '近景 · 硬币在手', seconds: '3 秒' },
];

const Home = () => {
  const authenticated = Boolean(localStorage.getItem('user'));
  const entryPath = authenticated ? '/console/mujian/projects' : '/register';

  return (
    <main className='mujian-home'>
      <section className='mujian-home-hero'>
        <div className='mujian-home-copy'>
          <div className='mujian-home-kicker'>
            <Sparkles size={15} /> AI 短剧创作工作台
          </div>
          <h1>
            一个想法，<span>完成剧本、分镜和画面。</span>
          </h1>
          <p>
            从故事梗概到完整剧本，再拆成可生成的分镜画面。随时用自然语言调整，Agent
            会把修改同步回整个项目。
          </p>
          <div className='mujian-home-actions'>
            <Link className='mujian-home-primary' to={entryPath}>
              立即开始创作 <ArrowRight size={17} />
            </Link>
            <Link
              className='mujian-home-secondary'
              to={authenticated ? '/console/mujian/projects' : '/login'}
            >
              查看完整案例
            </Link>
          </div>
          <div className='mujian-home-trust'>
            <span>
              <Check size={14} /> 无需信用卡
            </span>
            <span>
              <Check size={14} /> 新用户 1280 积分
            </span>
            <span>
              <Check size={14} /> 随时导出
            </span>
          </div>
        </div>

        <div
          className='mujian-home-pipeline'
          aria-label='剧本、分镜与图片生成流程'
        >
          <article className='mujian-flow-card script'>
            <header>
              <b>01</b>
              <strong>剧本创作</strong>
              <em>
                <Check size={13} /> 已完成
              </em>
            </header>
            <small>场景 07 · 内景 · 便利店 · 夜</small>
            <h3>一枚硬币，转过最后一圈。</h3>
            <p>雨砸在玻璃上，霓虹被拉成模糊的红线。</p>
            <blockquote>
              <b>林夏</b> 你早就知道，是不是？
              <br />
              <b>周沉</b> 我只是认得它的另一面。
            </blockquote>
          </article>

          <article className='mujian-flow-card storyboard'>
            <header>
              <b>02</b>
              <strong>分镜拆解</strong>
              <em>4 镜头 · 12 秒</em>
            </header>
            {shots.map((shot) => (
              <div className='mujian-home-shot' key={shot.index}>
                <span>{shot.index}</span>
                <i />
                <strong>{shot.title}</strong>
                <small>{shot.seconds}</small>
              </div>
            ))}
          </article>

          <article className='mujian-flow-card image'>
            <div className='mujian-home-frame'>
              <img src='/cover-4.webp' alt='AI 生成的短剧分镜画面' />
              <span>
                <Sparkles size={13} /> 画面生成完成
              </span>
            </div>
            <footer>
              <b>03</b>
              <strong>图片生成</strong>
              <em>Nano Banana · 9:16</em>
            </footer>
          </article>
        </div>
      </section>

      <section className='mujian-home-metrics'>
        <div>
          <strong>剧本生成</strong>
          <span>从梗概到场景台词</span>
        </div>
        <div>
          <strong>分镜拆解</strong>
          <span>运镜、景别与时长</span>
        </div>
        <div>
          <strong>多模型生图</strong>
          <span>按画面需求切换</span>
        </div>
        <div>
          <strong>Agent 协作</strong>
          <span>修改同步到全项目</span>
        </div>
      </section>

      <section className='mujian-home-features'>
        <div className='mujian-home-section-title'>
          <span>不是表单，是搭档</span>
          <h2>你说怎么改，它就从故事改到画面。</h2>
        </div>
        <div className='mujian-home-feature-grid'>
          <article>
            <WandSparkles />
            <h3>一直在线的创作 Agent</h3>
            <p>
              不论正在写剧本还是拆分镜，都能用一句话调整，修改会保留项目上下文。
            </p>
          </article>
          <article>
            <MessageSquareText />
            <h3>可见、可控的 Skills</h3>
            <p>编剧、分镜和画面提示词按任务组合，清楚知道 Agent 正在做什么。</p>
          </article>
          <article>
            <Image />
            <h3>统一的多模型生成</h3>
            <p>
              Claude、GPT、Grok、DeepSeek 与 Nano 系列统一经过 NewAPI
              路由和计费。
            </p>
          </article>
        </div>
      </section>

      <section className='mujian-home-cta'>
        <div>
          <span>你的下一部短剧</span>
          <h2>从一句话开始，在一次次对话里成形。</h2>
        </div>
        <Link className='mujian-home-primary' to={entryPath}>
          开始第一个项目 <ArrowRight size={17} />
        </Link>
      </section>
    </main>
  );
};

export default Home;
