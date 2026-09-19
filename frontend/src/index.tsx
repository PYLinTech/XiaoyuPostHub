import { fetchSiteConfig, fetchUserInfo } from '@/api/endpoints';
import '@/api/client';
import './style/global.less';
import '@arco-design/web-react/dist/css/arco.css';
import React, { Suspense, useEffect, useState } from 'react';
import ReactDOM from 'react-dom';
import { ConfigProvider } from '@arco-design/web-react';
import zhCN from '@arco-design/web-react/es/locale/zh-CN';
import enUS from '@arco-design/web-react/es/locale/en-US';
import { BrowserRouter, Switch, Route, useLocation } from 'react-router-dom';
import PageLayout from './layout';
import ErrorBoundary from './components/ErrorBoundary';
import { GlobalContext, UserInfo } from './context';
import changeTheme from './utils/changeTheme';
import useStorage from './utils/useStorage';
import projectSettings from './settings.json';

/**
 * 路由级错误边界：以 pathname 作 key，切换路由即自动复位，
 * 避免一个页面出错后整个应用都停在错误页。
 */
function RouteBoundary({ children }: { children: React.ReactNode }) {
  const location = useLocation();
  return <ErrorBoundary key={location.pathname}>{children}</ErrorBoundary>;
}

// 登录页与公开页面独立分包：已登录用户不必加载登录页的轮播等专属组件。
const Login = React.lazy(() => import('./pages/login'));
const PublicSharePage = React.lazy(() => import('./pages/share'));
const PickupPage = React.lazy(() => import('./pages/pickup'));

function Index() {
  const [lang, setLang] = useStorage('xph-lang', 'zh-CN');
  const [siteConfig, setSiteConfig] = useState({
    siteName: 'XiaoyuPostHub',
    siteIconUrl: '',
  });
  const privatePage =
    window.location.pathname !== '/login' &&
    window.location.pathname !== '/m' &&
    !window.location.pathname.startsWith('/s/');

  const [userInfo, setUserInfo] = useState<UserInfo>();
  const [userLoading, setUserLoading] = useState(privatePage);

  function getArcoLocale() {
    switch (lang) {
      case 'zh-CN':
        return zhCN;
      case 'en-US':
        return enUS;
      default:
        return zhCN;
    }
  }

  function loadUserInfo() {
    setUserLoading(true);
    fetchUserInfo()
      .then((res) => {
        setUserInfo(res.data);
        setUserLoading(false);
      })
      .catch(() => {
        if (window.location.pathname !== '/login') {
          window.location.replace('/login');
        }
      });
  }

  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);

  useEffect(() => {
    if (privatePage) {
      loadUserInfo();
    }
  }, [privatePage]);

  useEffect(() => {
    changeTheme(projectSettings.theme, projectSettings.themeColor);
  }, []);

  useEffect(() => {
    fetchSiteConfig().then((res) => {
      const next = {
        siteName: res.data.siteName || 'XiaoyuPostHub',
        siteIconUrl: res.data.siteIconUrl || '',
      };
      setSiteConfig(next);
      document.title = next.siteName;
      const favicon = document.querySelector('link[rel="icon"]');
      if (favicon instanceof HTMLLinkElement) {
        favicon.href = next.siteIconUrl || '/favicon.svg';
      }
    });
  }, []);

  const contextValue = {
    lang,
    setLang,
    ...siteConfig,
    userInfo,
    userLoading,
    setSiteConfig: (value) => {
      const next = { ...siteConfig, ...value };
      setSiteConfig(next);
      document.title = next.siteName;
      const favicon = document.querySelector('link[rel="icon"]');
      if (favicon instanceof HTMLLinkElement) {
        favicon.href = next.siteIconUrl || '/favicon.svg';
      }
    },
  };

  return (
    <BrowserRouter>
      <ConfigProvider
        locale={getArcoLocale()}
        componentConfig={{
          Card: {
            bordered: false,
          },
          List: {
            bordered: false,
          },
          Table: {
            border: false,
          },
        }}
      >
        <GlobalContext.Provider value={contextValue}>
          <RouteBoundary>
            <Switch>
              <Route path="/login">
                <Suspense fallback={<div style={{ minHeight: '100vh' }} />}>
                  <Login />
                </Suspense>
              </Route>
              <Route path="/s/:token">
                <Suspense fallback={<div style={{ minHeight: '100vh' }} />}>
                  <PublicSharePage />
                </Suspense>
              </Route>
              <Route exact path="/m">
                <Suspense fallback={<div style={{ minHeight: '100vh' }} />}>
                  <PickupPage />
                </Suspense>
              </Route>
              <Route path="/" component={PageLayout} />
            </Switch>
          </RouteBoundary>
        </GlobalContext.Provider>
      </ConfigProvider>
    </BrowserRouter>
  );
}

ReactDOM.render(<Index />, document.getElementById('root'));
