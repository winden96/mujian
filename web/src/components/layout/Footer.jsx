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

  const customFooter = useMemo(
    () => (
      <footer className='relative h-auto py-16 px-6 md:px-24 w-full flex flex-col items-center justify-between overflow-hidden'>
        <div className='absolute hidden md:block top-[204px] left-[-100px] w-[151px] h-[151px] rounded-full bg-[#FFD166]'></div>
        <div className='absolute md:hidden bottom-[20px] left-[-50px] w-[80px] h-[80px] rounded-full bg-[#FFD166] opacity-60'></div>

        {isDemoSiteMode && (
          <div className='flex flex-col md:flex-row justify-between w-full max-w-[1110px] mb-10 gap-8'>
            <div className='flex-shrink-0'>
              <img
                src={logo}
                alt={brandName}
                className='w-16 h-16 rounded-full bg-gray-800 p-1.5 object-contain'
              />
            </div>

            <div className='grid grid-cols-1 sm:grid-cols-2 md:grid-cols-4 gap-8 w-full'>
              <div className='text-left'>
                <p className='!text-semi-color-text-0 font-semibold mb-5'>
                  {t('幕间 AI')}
                </p>
                <div className='flex flex-col gap-4'>
                  <a href='/about' className='!text-semi-color-text-1'>
                    {t('关于幕间')}
                  </a>
                  <a
                    href='/console/mujian/projects'
                    className='!text-semi-color-text-1'
                  >
                    {t('创作项目')}
                  </a>
                  <a href='/pricing' className='!text-semi-color-text-1'>
                    {t('模型与价格')}
                  </a>
                </div>
              </div>

              <div className='text-left'>
                <p className='!text-semi-color-text-0 font-semibold mb-5'>
                  {t('创作指南')}
                </p>
                <div className='flex flex-col gap-4'>
                  <a
                    href='/console/mujian/projects'
                    className='!text-semi-color-text-1'
                  >
                    {t('开始创作')}
                  </a>
                  <a
                    href='/console/mujian/skills'
                    className='!text-semi-color-text-1'
                  >
                    {t('创作技能')}
                  </a>
                  <a
                    href='/console/mujian/integrations'
                    className='!text-semi-color-text-1'
                  >
                    {t('客户端接入')}
                  </a>
                </div>
              </div>

              <div className='text-left'>
                <p className='!text-semi-color-text-0 font-semibold mb-5'>
                  {t('创作资源')}
                </p>
                <div className='flex flex-col gap-4'>
                  <a
                    href='/console/mujian/wallet'
                    className='!text-semi-color-text-1'
                  >
                    {t('创作钱包')}
                  </a>
                  <a href='/pricing' className='!text-semi-color-text-1'>
                    {t('模型广场')}
                  </a>
                  <a
                    href={sourceUrl || 'https://github.com/winden96/mujian'}
                    target='_blank'
                    rel='noopener noreferrer'
                    className='!text-semi-color-text-1'
                  >
                    {t('开源说明')}
                  </a>
                </div>
              </div>

              <div className='text-left'>
                <p className='!text-semi-color-text-0 font-semibold mb-5'>
                  {t('服务与条款')}
                </p>
                <div className='flex flex-col gap-4'>
                  <a href='/user-agreement' className='!text-semi-color-text-1'>
                    {t('用户协议')}
                  </a>
                  <a href='/privacy-policy' className='!text-semi-color-text-1'>
                    {t('隐私政策')}
                  </a>
                  <a
                    href={sourceUrl || 'https://github.com/winden96/mujian'}
                    className='!text-semi-color-text-1'
                  >
                    {t('源代码')}
                  </a>
                </div>
              </div>
            </div>
          </div>
        )}

        <div className='flex flex-col md:flex-row items-center justify-between w-full max-w-[1110px] gap-6'>
          <div className='flex flex-wrap items-center gap-2'>
            <Typography.Text className='text-sm !text-semi-color-text-1'>
              © {currentYear} {brandName}. {t('版权所有')}
            </Typography.Text>
          </div>

          <div className='text-sm'>
            <span className='!text-semi-color-text-1'>
              开源组件遵循 AGPL v3 ·{' '}
            </span>
            <a
              href={sourceUrl || 'https://github.com/winden96/mujian'}
              target='_blank'
              rel='noopener noreferrer'
              className='!text-semi-color-primary font-medium'
            >
              {t('查看源码')}
            </a>
            {sourceUrl && (
              <>
                <span className='!text-semi-color-text-1'> · </span>
                <a
                  href={sourceUrl}
                  target='_blank'
                  rel='noopener noreferrer'
                  className='!text-semi-color-primary font-medium'
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
    <div className='w-full'>
      {footer ? (
        <footer className='relative h-auto py-4 px-6 md:px-24 w-full flex items-center justify-center overflow-hidden'>
          <div className='flex flex-col md:flex-row items-center justify-between w-full max-w-[1110px] gap-4'>
            <div
              className='custom-footer na-cb6feafeb3990c78 text-sm !text-semi-color-text-1'
              dangerouslySetInnerHTML={{ __html: footer }}
            ></div>
            <div className='text-sm flex-shrink-0'>
              <span className='!text-semi-color-text-1'>
                开源组件遵循 AGPL v3 ·{' '}
              </span>
              <a
                href={sourceUrl || 'https://github.com/winden96/mujian'}
                target='_blank'
                rel='noopener noreferrer'
                className='!text-semi-color-primary font-medium'
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
