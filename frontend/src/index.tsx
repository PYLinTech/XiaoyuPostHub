import './style/global.less';
import '@arco-design/web-react/dist/css/arco.css';
import React, { Suspense, useEffect, useState } from 'react';
import ReactDOM from 'react-dom';
import { ConfigProvider } from '@arco-design/web-react';
import zhCN from '@arco-design/web-react/es/locale/zh-CN';
import enUS from '@arco-design/web-react/es/locale/en-US';
import { BrowserRouter, Switch, Route } from 'react-router-dom';
import axios from 'axios';
import PageLayout from './layout';
import { GlobalContext, UserInfo } from './context';
import Login from './pages/login';
import changeTheme from './utils/changeTheme';
import useStorage from './utils/useStorage';
import projectSettings from './settings.json';
import { uiServerText } from './utils/uiText';

axios.interceptors.response.use(undefined, (error) => {
  if (typeof error?.response?.data?.msg === 'string') {
    error.response.data.msg = uiServerText(error.response.data.msg);
  }
  const path = window.location.pathname;
  if (
    error?.response?.status === 401 &&
    path !== '/login' &&
    !path.startsWith('/s/') &&
    !path.startsWith('/d/')
  ) {
    window.location.replace('/login');
  }
  return Promise.reject(error);
});
const PublicSharePage = React.lazy(() => import('./pages/share'));
const PublicDirectDownloadPage = React.lazy(
  () => import('./pages/direct-download')
);

function Index() {
  const [lang, setLang] = useStorage('xph-lang', 'zh-CN');
  const [siteConfig, setSiteConfig] = useState({
    siteName: 'XiaoyuPostHub',
    siteIconUrl: '',
  });
  const privatePage =
    window.location.pathname !== '/login' &&
    !window.location.pathname.startsWith('/s/') &&
    !window.location.pathname.startsWith('/d/');
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

  function fetchUserInfo() {
    setUserLoading(true);
    axios
      .get('/api/user/userInfo')
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
      fetchUserInfo();
    }
  }, [privatePage]);

  useEffect(() => {
    changeTheme(projectSettings.theme, projectSettings.themeColor);
    // 清除旧版本曾保存的明文登录凭据。
    try {
      window.localStorage.removeItem('loginParams');
    } catch {
      // 浏览器禁用存储时无需处理。
    }
  }, []);

  useEffect(() => {
    axios.get('/api/site-config').then((res) => {
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
          <Switch>
            <Route path="/login" component={Login} />
            <Route path="/s/:token">
              <Suspense fallback={<div style={{ minHeight: '100vh' }} />}>
                <PublicSharePage />
              </Suspense>
            </Route>
            <Route path="/d/:token">
              <Suspense fallback={<div style={{ minHeight: '100vh' }} />}>
                <PublicDirectDownloadPage />
              </Suspense>
            </Route>
            <Route path="/" component={PageLayout} />
          </Switch>
        </GlobalContext.Provider>
      </ConfigProvider>
    </BrowserRouter>
  );
}

ReactDOM.render(<Index />, document.getElementById('root'));
