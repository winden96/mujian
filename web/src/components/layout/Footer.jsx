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

import React, { useEffect, useState, useMemo, useContext } from 'react';
import DOMPurify from 'dompurify';
import { useTranslation } from 'react-i18next';
import { Typography } from '@douyinfe/semi-ui';
import { getFooterHTML, getLogo } from '../../helpers';
import { StatusContext } from '../../context/Status';

const FooterBar = () => {
  const { t } = useTranslation();
  const [footer, setFooter] = useState(getFooterHTML());
  const brandName = '幕间 AI';
  const logo = getLogo();
  const [statusState] = useContext(StatusContext);
  const isDemoSiteMode = statusState?.status?.demo_site_enabled || false;
  const sourceUrl = import.meta.env.VITE_AGPL_SOURCE_URL;

  const loadFooter = () => {
    let footer_html = localStorage.getItem('footer_html');
    if (footer_html) {
      setFooter(footer_html);
    }
  };

  const currentYear = new Date().getFullYear();
  const safeFooter = useMemo(
    () =>
      DOMPurify.sanitize(footer, {
        USE_PROFILES: { html: true },
      }),
    [footer],
  );

  const customFooter = useMemo(
    () => (
      <footer className='mujian-footer'>
        {isDemoSiteMode && (
          <div className='mujian-footer-directory'>
            <div className='mujian-footer-brand'>
              <img src={logo} alt={brandName} className='mujian-footer-logo' />
            </div>

            <div className='mujian-footer-links'>
              <div className='mujian-footer-group'>
                <p className='mujian-footer-heading'>{t('幕间 AI')}</p>
                <div className='mujian-footer-link-list'>
                  <a href='/about'>{t('关于幕间')}</a>
                  <a href='/console/mujian/projects'>{t('创作项目')}</a>
                  <a href='/pricing'>{t('模型与价格')}</a>
                </div>
              </div>

              <div className='mujian-footer-group'>
                <p className='mujian-footer-heading'>{t('创作指南')}</p>
                <div className='mujian-footer-link-list'>
                  <a href='/console/mujian/projects'>{t('开始创作')}</a>
                  <a href='/console/mujian/skills'>{t('创作技能')}</a>
                  <a href='/console/token'>{t('API Key')}</a>
                  <a href='/docs'>{t('API 文档')}</a>
                </div>
              </div>

              <div className='mujian-footer-group'>
                <p className='mujian-footer-heading'>{t('创作资源')}</p>
                <div className='mujian-footer-link-list'>
                  <a href='/console/mujian/wallet'>{t('创作钱包')}</a>
                  <a href='/pricing'>{t('模型广场')}</a>
                  <a
                    href={sourceUrl || 'https://github.com/winden96/mujian'}
                    target='_blank'
                    rel='noopener noreferrer'
                  >
                    {t('开源说明')}
                  </a>
                </div>
              </div>

              <div className='mujian-footer-group'>
                <p className='mujian-footer-heading'>{t('服务与条款')}</p>
                <div className='mujian-footer-link-list'>
                  <a href='/user-agreement'>{t('用户协议')}</a>
                  <a href='/privacy-policy'>{t('隐私政策')}</a>
                  <a href={sourceUrl || 'https://github.com/winden96/mujian'}>
                    {t('源代码')}
                  </a>
                </div>
              </div>
            </div>
          </div>
        )}

        <div className='mujian-footer-bottom'>
          <div>
            <Typography.Text className='mujian-footer-copyright'>
              © {currentYear} {brandName}. {t('版权所有')}
            </Typography.Text>
          </div>

          <div className='mujian-footer-license'>
            <span>开源组件遵循 AGPL v3 · </span>
            <a
              href={sourceUrl || 'https://github.com/winden96/mujian'}
              target='_blank'
              rel='noopener noreferrer'
              className='mujian-footer-source'
            >
              {t('查看源码')}
            </a>
            {sourceUrl && (
              <>
                <span> · </span>
                <a
                  href={sourceUrl}
                  target='_blank'
                  rel='noopener noreferrer'
                  className='mujian-footer-source'
                >
                  {t('幕间 AI 源码')}
                </a>
              </>
            )}
          </div>
        </div>
      </footer>
    ),
    [logo, brandName, t, currentYear, isDemoSiteMode, sourceUrl],
  );

  useEffect(() => {
    loadFooter();
  }, []);

  return (
    <div className='mujian-footer-shell'>
      {footer ? (
        <footer className='mujian-footer mujian-footer--compact'>
          <div className='mujian-footer-bottom'>
            <div
              className='custom-footer na-cb6feafeb3990c78 mujian-footer-custom'
              dangerouslySetInnerHTML={{ __html: safeFooter }}
            ></div>
            <div className='mujian-footer-license'>
              <span>开源组件遵循 AGPL v3 · </span>
              <a
                href={sourceUrl || 'https://github.com/winden96/mujian'}
                target='_blank'
                rel='noopener noreferrer'
                className='mujian-footer-source'
              >
                {t('查看源码')}
              </a>
            </div>
          </div>
        </footer>
      ) : (
        customFooter
      )}
    </div>
  );
};

export default FooterBar;
