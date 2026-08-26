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

import HeaderBar from './headerbar';
import { Layout } from '@douyinfe/semi-ui';
import SiderBar from './SiderBar';
import App from '../../App';
import FooterBar from './Footer';
import { ToastContainer } from 'react-toastify';
import ErrorBoundary from '../common/ErrorBoundary';
import React, {
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react';
import { useIsMobile } from '../../hooks/common/useIsMobile';
import { useSidebarCollapsed } from '../../hooks/common/useSidebarCollapsed';
import { useTranslation } from 'react-i18next';
import {
  API,
  getLogo,
  getSystemName,
  showError,
  setStatusData,
} from '../../helpers';
import { UserContext } from '../../context/User';
import { StatusContext } from '../../context/Status';
import { matchPath, useLocation } from 'react-router-dom';
import { normalizeLanguage } from '../../i18n/language';
import './layout-shell.css';
const { Sider, Content, Header } = Layout;

const PUBLIC_ROUTES = new Set([
  '/',
  '/pricing',
  '/docs',
  '/about',
  '/user-agreement',
  '/privacy-policy',
]);

const AUTH_ROUTES = new Set(['/login', '/register', '/reset', '/user/reset']);

const isWorkspaceRoute = (pathname) =>
  Boolean(
    matchPath('/console/mujian/projects/:projectId/workspace', pathname),
  ) ||
  pathname === '/console/playground' ||
  pathname.startsWith('/console/chat') ||
  pathname === '/chat2link';

export const getLayoutMode = (pathname) => {
  if (AUTH_ROUTES.has(pathname) || pathname.startsWith('/oauth/')) {
    return 'auth';
  }

  if (isWorkspaceRoute(pathname)) {
    return 'workspace';
  }

  if (pathname.startsWith('/console')) {
    return 'console';
  }

  if (PUBLIC_ROUTES.has(pathname)) {
    return 'public';
  }

  return 'system';
};

const PageLayout = () => {
  const [userState, userDispatch] = useContext(UserContext);
  const [, statusDispatch] = useContext(StatusContext);
  const isMobile = useIsMobile();
  const [collapsed, , setCollapsed] = useSidebarCollapsed();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const restoreDrawerFocus = useRef(false);
  const { i18n, t } = useTranslation();
  const location = useLocation();
  const layoutMode = getLayoutMode(location.pathname);
  const sidebarEnabled = layoutMode === 'console';
  const shouldShowFooter =
    layoutMode === 'public' ||
    (layoutMode === 'system' && location.pathname !== '/setup');
  const shouldInnerPadding =
    layoutMode === 'console' &&
    !location.pathname.startsWith('/console/mujian');

  const showSider = sidebarEnabled && (!isMobile || drawerOpen);

  const closeMobileDrawer = useCallback((restoreFocus = false) => {
    restoreDrawerFocus.current = restoreFocus;
    setDrawerOpen(false);
  }, []);

  useEffect(() => {
    if (drawerOpen || !restoreDrawerFocus.current) return;
    restoreDrawerFocus.current = false;
    document.getElementById('app-sidebar-toggle')?.focus();
  }, [drawerOpen]);

  useEffect(() => {
    if (isMobile && drawerOpen && collapsed) {
      setCollapsed(false);
    }
  }, [isMobile, drawerOpen, collapsed, setCollapsed]);

  useEffect(() => {
    if (!drawerOpen) return undefined;

    const closeOnEscape = (event) => {
      if (event.key === 'Escape') {
        closeMobileDrawer(true);
      }
    };

    const drawer = document.getElementById('app-console-sidebar');
    const focusable = Array.from(
      drawer?.querySelectorAll(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) || [],
    );
    const firstFocusable = focusable[0];
    const lastFocusable = focusable[focusable.length - 1];

    const trapFocus = (event) => {
      if (event.key !== 'Tab' || !firstFocusable || !lastFocusable) return;
      if (event.shiftKey && document.activeElement === firstFocusable) {
        event.preventDefault();
        lastFocusable.focus();
      } else if (!event.shiftKey && document.activeElement === lastFocusable) {
        event.preventDefault();
        firstFocusable.focus();
      }
    };

    window.addEventListener('keydown', closeOnEscape);
    drawer?.addEventListener('keydown', trapFocus);
    firstFocusable?.focus();

    return () => {
      window.removeEventListener('keydown', closeOnEscape);
      drawer?.removeEventListener('keydown', trapFocus);
    };
  }, [closeMobileDrawer, drawerOpen, isMobile]);

  useEffect(() => {
    setDrawerOpen(false);
  }, [location.pathname]);

  const loadUser = () => {
    let user = localStorage.getItem('user');
    if (user) {
      let data = JSON.parse(user);
      userDispatch({ type: 'login', payload: data });
    }
  };

  const loadStatus = async () => {
    try {
      const res = await API.get('/api/status');
      const { success, data } = res.data;
      if (success) {
        statusDispatch({ type: 'set', payload: data });
        setStatusData(data);
        if (data.system_name) {
          document.title = data.system_name;
        }
      } else {
        showError('Unable to connect to server');
      }
    } catch (error) {
      showError('Failed to load status');
    }
  };

  useEffect(() => {
    loadUser();
    loadStatus().catch(console.error);
    let systemName = getSystemName();
    if (systemName) {
      document.title = systemName;
    }
    let logo = getLogo();
    if (logo) {
      let linkElement = document.querySelector("link[rel~='icon']");
      if (linkElement) {
        linkElement.href = logo;
      }
    }
  }, []);

  useEffect(() => {
    let preferredLang;

    if (userState?.user?.setting) {
      try {
        const settings = JSON.parse(userState.user.setting);
        preferredLang = normalizeLanguage(settings.language);
      } catch (e) {
        // Ignore parse errors
      }
    }

    if (!preferredLang) {
      const savedLang = localStorage.getItem('i18nextLng');
      if (savedLang) {
        preferredLang = normalizeLanguage(savedLang);
      }
    }

    if (preferredLang) {
      localStorage.setItem('i18nextLng', preferredLang);
      if (preferredLang !== i18n.language) {
        i18n.changeLanguage(preferredLang);
      }
    }
  }, [i18n, userState?.user?.setting]);

  useEffect(() => {
    const language = normalizeLanguage(i18n.resolvedLanguage || i18n.language);
    document.documentElement.lang = language || 'zh-CN';
  }, [i18n.language, i18n.resolvedLanguage]);

  return (
    <Layout
      className={`app-layout app-layout--${layoutMode}`}
      data-layout-mode={layoutMode}
    >
      <Header className='app-header-shell'>
        <HeaderBar
          onMobileMenuToggle={() => setDrawerOpen((prev) => !prev)}
          drawerOpen={drawerOpen}
          sidebarEnabled={sidebarEnabled}
        />
      </Header>
      <Layout className='app-shell'>
        {isMobile && drawerOpen && sidebarEnabled && (
          <button
            type='button'
            className='app-sider-backdrop'
            aria-label={t('关闭侧边栏')}
            tabIndex={-1}
            onClick={() => closeMobileDrawer(true)}
          />
        )}
        {showSider && (
          <Sider
            className={`app-sider${isMobile ? ' app-sider--mobile' : ''}`}
            aria-label={t('控制台')}
            role={isMobile ? 'dialog' : undefined}
            aria-modal={isMobile ? 'true' : undefined}
            style={{
              width: 'var(--sidebar-current-width)',
            }}
          >
            <SiderBar
              onNavigate={() => {
                if (isMobile) closeMobileDrawer();
              }}
            />
          </Sider>
        )}
        <Layout
          className='app-shell-stage'
          style={{
            marginLeft: isMobile
              ? '0'
              : showSider
                ? 'var(--sidebar-current-width)'
                : '0',
          }}
        >
          <Content
            className={`app-shell-content${
              shouldInnerPadding ? ' app-shell-content--padded' : ''
            }`}
          >
            <ErrorBoundary>
              <App />
            </ErrorBoundary>
          </Content>
          {shouldShowFooter && (
            <Layout.Footer className='app-shell-footer'>
              <FooterBar />
            </Layout.Footer>
          )}
        </Layout>
      </Layout>
      <ToastContainer />
    </Layout>
  );
};

export default PageLayout;
