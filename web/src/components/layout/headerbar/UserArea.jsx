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

import React, { useRef } from 'react';
import { Link } from 'react-router-dom';
import { Avatar, Button, Dropdown, Typography } from '@douyinfe/semi-ui';
import { ChevronDown } from 'lucide-react';
import { IconExit, IconUserSetting, IconKey } from '@douyinfe/semi-icons';
import { isAdmin, stringToColor } from '../../../helpers';
import SkeletonWrapper from '../components/SkeletonWrapper';

const UserArea = ({
  userState,
  isLoading,
  isMobile,
  isSelfUseMode,
  logout,
  navigate,
  t,
}) => {
  const dropdownRef = useRef(null);
  if (isLoading) {
    return (
      <SkeletonWrapper
        loading={true}
        type='userArea'
        width={50}
        isMobile={isMobile}
      />
    );
  }

  if (userState.user) {
    return (
      <div className='relative' ref={dropdownRef}>
        <Dropdown
          position='bottomRight'
          getPopupContainer={() => dropdownRef.current}
          render={
            <Dropdown.Menu className='mujian-header-menu'>
              {isAdmin() && (
                <Dropdown.Item
                  onClick={() => {
                    navigate('/console/personal');
                  }}
                  className='mujian-header-menu-item'
                >
                  <div className='flex items-center gap-2'>
                    <IconUserSetting
                      size='small'
                      className='mujian-header-menu-icon'
                    />
                    <span>{t('安全设置')}</span>
                  </div>
                </Dropdown.Item>
              )}
              {isAdmin() && (
                <Dropdown.Item
                  onClick={() => {
                    navigate('/console/token');
                  }}
                  className='mujian-header-menu-item'
                >
                  <div className='flex items-center gap-2'>
                    <IconKey size='small' className='mujian-header-menu-icon' />
                    <span>{t('令牌管理')}</span>
                  </div>
                </Dropdown.Item>
              )}
              <Dropdown.Item
                onClick={logout}
                className='mujian-header-menu-item mujian-header-menu-item--danger'
              >
                <div className='flex items-center gap-2'>
                  <IconExit size='small' className='mujian-header-menu-icon' />
                  <span>{t('退出')}</span>
                </div>
              </Dropdown.Item>
            </Dropdown.Menu>
          }
        >
          <Button
            theme='borderless'
            type='tertiary'
            className='mujian-user-trigger'
            aria-label={t('用户菜单')}
          >
            <span aria-hidden='true'>
              <Avatar
                size='extra-small'
                color={stringToColor(userState.user.username)}
                className='mr-1'
              >
                {userState.user.username[0].toUpperCase()}
              </Avatar>
            </span>
            <span className='hidden md:inline'>
              <Typography.Text className='mujian-user-name'>
                {userState.user.username}
              </Typography.Text>
            </span>
            <ChevronDown size={14} className='mujian-user-chevron' />
          </Button>
        </Dropdown>
      </div>
    );
  } else {
    const showRegisterButton = !isSelfUseMode;

    return (
      <div className='mujian-auth-actions'>
        <Link to='/login' className='flex'>
          <Button
            theme='borderless'
            type='tertiary'
            className='mujian-auth-action mujian-auth-action--login'
          >
            <span>{t('登录')}</span>
          </Button>
        </Link>
        {showRegisterButton && (
          <div className='hidden md:block'>
            <Link to='/register' className='flex'>
              <Button
                theme='solid'
                type='primary'
                className='mujian-auth-action mujian-auth-action--register'
              >
                <span>{t('注册')}</span>
              </Button>
            </Link>
          </div>
        )}
      </div>
    );
  }
};

export default UserArea;
