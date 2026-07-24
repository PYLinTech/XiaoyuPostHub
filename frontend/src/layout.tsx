import React, { Suspense, useContext, useEffect, useState } from 'react';
import { Redirect, Route, Switch, useHistory } from 'react-router-dom';
import { Button, Layout, Menu, Modal, Spin } from '@arco-design/web-react';
import cs from 'classnames';
import {
  IconClose,
  IconDashboard,
  IconDelete,
  IconFolder,
  IconHistory,
  IconLink,
  IconMenuFold,
  IconMenuUnfold,
  IconSafe,
  IconSettings,
  IconShareAlt,
  IconUserGroup,
} from '@arco-design/web-react/icon';
import Navbar, { MobileNavActions } from './components/NavBar';
import Footer from './components/Footer';
import { getRoutesForUser, IRoute } from '@/routes';
import useLocale from './utils/useLocale';
import { GlobalContext } from './context';
import projectSettings from './settings.json';
import AccessDenied from './pages/access-denied';
import styles from './style/layout.module.less';
import uiText from '@/utils/uiText';
import { UploadProvider } from '@/components/UploadManager';
const Sider = Layout.Sider;
const Content = Layout.Content;
const routePages = {
  files: React.lazy(() => import('./pages/files')),
  shares: React.lazy(() => import('./pages/shares')),
  'direct-links': React.lazy(() => import('./pages/direct-links')),
  trash: React.lazy(() => import('./pages/trash')),
  messages: React.lazy(() => import('./pages/messages')),
  'admin/overview': React.lazy(() => import('./pages/admin/overview')),
  'admin/users': React.lazy(() => import('./pages/admin/users')),
  'admin/access': React.lazy(() => import('./pages/admin/access')),
  'admin/audit': React.lazy(() => import('./pages/admin/audit')),
  'admin/system': React.lazy(() => import('./pages/admin/system')),
};
function getIcon(key: string) {
  const icons = {
    files: IconFolder,
    shares: IconShareAlt,
    'direct-links': IconLink,
    trash: IconDelete,
    'admin/overview': IconDashboard,
    'admin/users': IconUserGroup,
    'admin/access': IconSafe,
    'admin/audit': IconHistory,
    'admin/system': IconSettings,
  };
  const Icon = icons[key];
  return Icon ? <Icon className={styles.icon} /> : null;
}
function PageLayout() {
  const history = useHistory();
  const pathname = history.location.pathname;
  const locale = useLocale();
  const { userLoading, userInfo } = useContext(GlobalContext);
  const adminMode =
    pathname.startsWith('/admin') ||
    (pathname === '/' && Boolean(userInfo?.isSuperAdmin));
  const [visibleRoutes, defaultRoute] = getRoutesForUser(userInfo, adminMode);
  const currentRoute = visibleRoutes.find(
    (route) => pathname === `/${route.key}`
  );
  const [collapsed, setCollapsed] = useState(false);
  const [mobileMenuVisible, setMobileMenuVisible] = useState(false);
  const [totpWarningVisible, setTOTPWarningVisible] = useState(false);
  useEffect(() => {
    if (userInfo?.requiresTOTPSetup) setTOTPWarningVisible(true);
  }, [userInfo?.requiresTOTPSetup]);
  const menuWidth = collapsed ? 48 : projectSettings.menuWidth;
  function openRoute(key: string) {
    history.push(`/${key}`);
    setMobileMenuVisible(false);
  }
  function renderMenuItem(route: IRoute) {
    return (
      <Menu.Item key={route.key}>
        {getIcon(route.key)} {locale[route.name] || route.name}
      </Menu.Item>
    );
  }
  return (
    <UploadProvider>
      <Modal title={uiText('请配置登录动态令牌')} visible={totpWarningVisible} footer={null}
        closable={false} maskClosable={false}>
        <p>{uiText('管理员已要求你的用户组使用登录动态令牌。你本次仍可继续使用，但下一次登录将强制验证，请务必现在完成配置。')}</p>
        <Button type="primary" long onClick={() => {
          setTOTPWarningVisible(false);
          window.dispatchEvent(new Event('xph-open-user-settings'));
        }}>{uiText('去配置')}</Button>
      </Modal>
      <Layout className={styles.layout}>
        <div className={styles['layout-navbar']}>
          <Navbar
            mobileMenuVisible={mobileMenuVisible}
            onToggleMobileMenu={() => {
              setCollapsed(false);
              setMobileMenuVisible((visible) => !visible);
            }}
          />
        </div>
        {userLoading ? (
          <Spin className={styles.spin} />
        ) : (
          <Layout>
            <button
              type="button"
              aria-label={uiText('关闭导航菜单')}
              className={cs(styles['mobile-menu-mask'], {
                [styles['mobile-menu-mask-visible']]: mobileMenuVisible,
              })}
              onClick={() => setMobileMenuVisible(false)}
            />
            <Sider
              className={cs(styles['layout-sider'], {
                [styles['layout-sider-mobile-visible']]: mobileMenuVisible,
              })}
              width={menuWidth}
              collapsed={collapsed}
              onCollapse={setCollapsed}
              trigger={null}
              collapsible
              style={{
                paddingTop: 60,
              }}
            >
              <button
                type="button"
                className={styles['mobile-menu-close']}
                aria-label={uiText('关闭导航菜单')}
                onClick={() => setMobileMenuVisible(false)}
              >
                <IconClose />
              </button>
              <div className={styles['menu-wrapper']}>
                <Menu
                  collapse={collapsed}
                  onClickMenuItem={openRoute}
                  selectedKeys={currentRoute ? [currentRoute.key] : []}
                >
                  {visibleRoutes.map(renderMenuItem)}
                </Menu>
              </div>
              <MobileNavActions
                onNavigate={() => setMobileMenuVisible(false)}
              />
              <button
                type="button"
                aria-label={
                  collapsed ? uiText('展开导航菜单') : uiText('收起导航菜单')
                }
                className={styles['collapse-btn']}
                onClick={() => setCollapsed((value) => !value)}
              >
                {collapsed ? <IconMenuUnfold /> : <IconMenuFold />}
              </button>
            </Sider>
            <Layout
              className={styles['layout-content']}
              style={{
                paddingLeft: menuWidth,
                paddingTop: 60,
              }}
            >
              <div className={styles['layout-content-wrapper']}>
                <Content>
                  <Suspense fallback={<Spin className={styles.spin} />}>
                    <Switch>
                      <Route
                        exact
                        path={['/messages', '/admin/messages']}
                        component={routePages.messages}
                      />
                      {visibleRoutes.map((route) => (
                        <Route
                          exact
                          key={route.key}
                          path={`/${route.key}`}
                          component={routePages[route.key]}
                        />
                      ))}
                      <Route exact path="/">
                        {defaultRoute ? (
                          <Redirect to={`/${defaultRoute}`} />
                        ) : (
                          <AccessDenied />
                        )}
                      </Route>
                      <Route path="*" component={AccessDenied} />
                    </Switch>
                  </Suspense>
                </Content>
              </div>
              <Footer />
            </Layout>
          </Layout>
        )}
      </Layout>
    </UploadProvider>
  );
}
export default PageLayout;
