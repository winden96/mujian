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

import React, { useEffect } from 'react';
import { Link } from 'react-router-dom';
import {
  ArrowRight,
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

const stories = [
  {
    title: '雨夜便利店',
    status: '正在连载',
    cover: '/stories/rain-store.jpg',
    logline: '雨声盖住了硬币落地的声音。',
  },
  {
    title: '末班车',
    status: '正在制作',
    cover: '/stories/last-train.jpg',
    logline: '车厢只剩他一个人。',
  },
  {
    title: '旧港灯塔',
    status: '正在制作',
    cover: '/stories/harbor.jpg',
    logline: '她把风向记进骨头里。',
  },
  {
    title: '楼顶天线',
    status: '正在制作',
    cover: '/stories/rooftop.jpg',
    logline: '城市亮起来之前，先说话。',
  },
  {
    title: '白衬衫',
    status: '正在制作',
    cover: '/stories/white-shirt.jpg',
    logline: '窗边那件衣服还没干。',
  },
];

const featured = stories[0];

const Home = () => {
  const authenticated = Boolean(localStorage.getItem('user'));
  const entryPath = authenticated ? '/console/mujian/projects' : '/register';

  useEffect(() => {
    document.body.classList.add('mujian-home-route');
    return () => document.body.classList.remove('mujian-home-route');
  }, []);

  return (
    <main className='mujian-home'>
      <section className='mujian-home-hero'>
        <figure className='mujian-home-hero-story'>
          <img
            src='/stories/rain-store-wide.jpg'
            alt={`${featured.title}：${featured.logline}`}
          />
        </figure>
        <div className='mujian-home-copy'>
          <p className='mujian-home-kicker'>SEEDANCE · 短剧正在连载</p>
          <h1>
            最让人停不下来的
            <em>AI 短剧。</em>
          </h1>
          <p>
            先有故事，再有镜头。幕间把一个念头做成可连载的短剧：角色、分镜、Seedance
            成片，都在同一条制作线上。
          </p>
          <div className='mujian-home-actions'>
            <Link className='mujian-home-primary' to={entryPath}>
              开始一个故事 <ArrowRight size={17} />
            </Link>
            <Link
              className='mujian-home-secondary'
              to={authenticated ? '/console/mujian/projects' : '/login'}
            >
              进入工作室
            </Link>
          </div>
        </div>
        <p className='mujian-home-hero-caption'>
          <span>{featured.status}</span>
          {featured.title} · {featured.logline}
        </p>
      </section>

      <section className='mujian-home-stories' aria-label='正在制作的故事'>
        <div className='mujian-home-stories-heading'>
          <span>Made with 幕间</span>
          <h2>正在制作的故事</h2>
        </div>
        <div className='mujian-home-story-grid'>
          {stories.map((story) => (
            <article key={story.title}>
              <img src={story.cover} alt={story.title} />
              <div>
                <span>{story.status}</span>
                <strong>{story.title}</strong>
              </div>
            </article>
          ))}
        </div>
      </section>

      <section className='mujian-home-proof' aria-label='幕间 AI 视频制作成果'>
        <div className='mujian-home-proof-heading'>
          <span>客户拿到的，不是一串提示词</span>
          <h2>
            是一条能继续推进的
            <br />
            视频制作线。
          </h2>
          <p>
            幕间把零散的创意需求组织成制作语言，让 Seedance
            在正确的场景、角色与镜头上下文中工作。
          </p>
        </div>
        <div className='mujian-home-proof-grid'>
          <article className='mujian-proof-brief'>
            <span>01 · VIDEO BRIEF</span>
            <h3>一页需求，变成可执行的拍摄表达。</h3>
            <div className='mujian-proof-tags'>
              <i>人物关系</i>
              <i>场景情绪</i>
              <i>时长节奏</i>
            </div>
          </article>
          <article className='mujian-proof-board'>
            <span>02 · SHOT BOARD</span>
            <div className='mujian-proof-strip'>
              <i />
              <i />
              <i />
              <i />
            </div>
            <p>每一镜都带着前一镜的角色、场景与情绪继续往前走。</p>
          </article>
          <article className='mujian-proof-delivery'>
            <span>03 · SEEDANCE DELIVERY</span>
            <strong>
              视频镜头
              <br />
              持续制作
            </strong>
            <p>保留项目上下文，能反复修改、继续生成和交付。</p>
          </article>
        </div>
      </section>

      <section className='mujian-home-studio' aria-label='幕间 AI 创作流程预览'>
        <div className='mujian-home-studio-heading'>
          <span>不是聊天框，是视频制作台</span>
          <h2>从需求、分镜，到每一个镜头。</h2>
        </div>
        <div className='mujian-home-stage'>
          <article className='mujian-stage-script'>
            <div className='mujian-stage-label'>
              <WandSparkles size={15} /> SCRIPT · 07
            </div>
            <small>便利店 / 深夜 / 雨</small>
            <h3>雨声盖住了硬币落地的声音。</h3>
            <p>她没有回头，只在玻璃倒影里看见那盏熟悉的灯亮起来。</p>
            <div className='mujian-stage-line' />
            <span>“有些告别，应该慢一点。”</span>
          </article>
          <article className='mujian-stage-storyboard'>
            <div className='mujian-stage-label'>
              <MessageSquareText size={15} /> STORYBOARD
            </div>
            {shots.map((shot) => (
              <div className='mujian-home-shot' key={shot.index}>
                <b>{shot.index}</b>
                <i />
                <strong>{shot.title}</strong>
                <small>{shot.seconds}</small>
              </div>
            ))}
          </article>
          <article className='mujian-stage-frame'>
            <img src='/stories/rain-store.jpg' alt='雨夜便利店分镜画面' />
            <span>
              <Sparkles size={13} /> FRAME · READY
            </span>
          </article>
        </div>
      </section>

      <section className='mujian-home-features'>
        <div className='mujian-home-section-title'>
          <span>让模型服务制作，而不是打断制作</span>
          <h2>改一句需求，脚本、分镜与镜头一起往前走。</h2>
        </div>
        <div className='mujian-home-feature-grid'>
          <article>
            <WandSparkles />
            <h3>需求不会在对话里丢失</h3>
            <p>每次修改都回到同一个视频项目，角色、场景和交付目标保持在场。</p>
          </article>
          <article>
            <MessageSquareText />
            <h3>把制作拆成可控步骤</h3>
            <p>
              需求、脚本、分镜和镜头各自有依据，团队知道每一步正在交付什么。
            </p>
          </article>
          <article>
            <Image />
            <h3>让 Seedance 专注做视频</h3>
            <p>前置创作信息准备充分，视频生成不再从一条孤立的提示词开始。</p>
          </article>
        </div>
      </section>

      <section className='mujian-home-cta'>
        <div>
          <span>准备好写第一个故事了吗</span>
          <h2>把故事交给幕间，沿着制作线交付成片。</h2>
        </div>
        <Link className='mujian-home-primary' to={entryPath}>
          开始一个故事 <ArrowRight size={17} />
        </Link>
      </section>
    </main>
  );
};

export default Home;
