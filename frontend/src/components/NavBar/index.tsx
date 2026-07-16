import React, { useContext } from 'react';
import axios from 'axios';
import {
  Select,
  Dropdown,
  Menu,
  Divider,
  Message,
} from '@arco-design/web-react';
import {
  IconLanguage,
  IconNotification,
  IconPoweroff,
  IconSafe,
  IconUser,
  IconMenu,
} from '@arco-design/web-react/icon';
import { useHistory } from 'react-router-dom';
import { GlobalContext } from '@/context';
import useLocale from '@/utils/useLocale';
import logoUrl from '@/assets/logo.svg';
import MessageBox from '@/components/MessageBox';
import IconButton from './IconButton';
import styles from './style/index.module.less';
import defaultLocale from '@/locale';
import { hasManagementAccess } from '@/routes';
import uiText from '@/utils/uiText';
function NavActions({
  mobile = false,
  onNavigate,
}: {
  mobile?: boolean;
  onNavigate?: () => void;
}) {
  const t = useLocale();
  const { setLang, lang, userInfo, userLoading } = useContext(GlobalContext);
  const history = useHistory();
  async function logout() {
    try {
      await axios.post('/api/user/logout');
      window.location.replace('/login');
    } catch (error) {
      Message.error(t['navbar.logout.failed']);
    }
  }
  function onMenuItemClick(key) {
    onNavigate?.();
    if (key === 'logout') {
      logout();
    } else if (key === 'identity-admin') {
      history.push('/admin/overview');
    } else if (key === 'identity-user') {
      history.push('/files');
    }
  }
  const adminMode = history.location.pathname.startsWith('/admin');
  const canSwitchIdentity = hasManagementAccess(userInfo);
  const droplist = (
    <Menu
      onClickMenuItem={onMenuItemClick}
      selectedKeys={[adminMode ? 'identity-admin' : 'identity-user']}
    >
      {canSwitchIdentity && (
        <Menu.SubMenu
          key="identity"
          title={
            <div className={styles['identity-title']}>
              <IconSafe className={styles['dropdown-icon']} />
              {t['menu.user.switchRoles']}
            </div>
          }
        >
          <Menu.Item key="identity-admin">
            <IconSafe className={styles['dropdown-icon']} />
            {t['menu.user.role.admin']}
          </Menu.Item>
          <Menu.Item key="identity-user">
            <IconUser className={styles['dropdown-icon']} />
            {t['menu.user.role.user']}
          </Menu.Item>
        </Menu.SubMenu>
      )}
      {canSwitchIdentity && (
        <Divider
          style={{
            margin: '4px 0',
          }}
        />
      )}
      <Menu.Item key="logout">
        <IconPoweroff className={styles['dropdown-icon']} />
        {t['navbar.logout']}
      </Menu.Item>
    </Menu>
  );
  const languageTrigger = mobile ? (
    <button
      type="button"
      className={styles['mobile-action-button']}
      aria-label={t['navbar.language']}
    >
      <IconLanguage />
      <span>{t['navbar.language']}</span>
    </button>
  ) : (
    <IconButton aria-label={uiText('切换语言')} icon={<IconLanguage />} />
  );
  const messageTrigger = mobile ? (
    <button
      type="button"
      className={styles['mobile-action-button']}
      aria-label={t['navbar.messages']}
    >
      <IconNotification />
      <span>{t['navbar.messages']}</span>
    </button>
  ) : (
    <IconButton
      aria-label={uiText('打开消息中心')}
      icon={<IconNotification />}
    />
  );
  return (
    <ul className={mobile ? styles['mobile-actions'] : styles.right}>
      <li>
        <Select
          triggerElement={languageTrigger}
          options={[
            {
              label: uiText('中文'),
              value: 'zh-CN',
            },
            {
              label: 'English',
              value: 'en-US',
            },
          ]}
          value={lang}
          triggerProps={{
            autoAlignPopupWidth: false,
            autoAlignPopupMinWidth: true,
            position: mobile ? 'rt' : 'br',
          }}
          trigger={mobile ? 'click' : 'hover'}
          onChange={(value) => {
            setLang(value);
            const nextLang = defaultLocale[value];
            Message.info(
              `${nextLang['message.lang.tips']}${
                value === 'zh-CN' ? '中文' : 'English'
              }`
            );
          }}
        />
      </li>
      <li>
        <MessageBox position={mobile ? 'rt' : 'br'}>
          {messageTrigger}
        </MessageBox>
      </li>
      {userInfo && (
        <li>
          <Dropdown
            droplist={droplist}
            position={mobile ? 'tr' : 'br'}
            trigger="click"
            disabled={userLoading}
          >
            {mobile ? (
              <button
                type="button"
                className={styles['mobile-action-button']}
                aria-label={t['navbar.account']}
              >
                <IconUser />
                <span className={styles['mobile-account-name']}>
                  {userInfo.name || t['menu.user.role.user']}
                </span>
              </button>
            ) : (
              <IconButton
                aria-label={t['navbar.account']}
                icon={<IconUser />}
              />
            )}
          </Dropdown>
        </li>
      )}
    </ul>
  );
}
export function MobileNavActions({ onNavigate }: { onNavigate?: () => void }) {
  return <NavActions mobile onNavigate={onNavigate} />;
}
function Navbar({
  mobileMenuVisible,
  onToggleMobileMenu,
}: {
  mobileMenuVisible?: boolean;
  onToggleMobileMenu?: () => void;
}) {
  const { siteName, siteIconUrl } = useContext(GlobalContext);
  return (
    <div className={styles.navbar}>
      <div className={styles.left}>
        <button
          type="button"
          aria-label={
            mobileMenuVisible ? uiText('关闭导航菜单') : uiText('打开导航菜单')
          }
          className={styles['mobile-menu-button']}
          onClick={onToggleMobileMenu}
        >
          <IconMenu />
        </button>
        <div className={styles.logo}>
          <img
            className={styles['site-logo']}
            src={siteIconUrl || logoUrl}
            alt={uiText('站点图标')}
          />
          <div className={styles['logo-name']}>
            {siteName || 'XiaoyuPostHub'}
          </div>
        </div>
      </div>
      <NavActions />
    </div>
  );
}
export default Navbar;
