import React, { useContext } from 'react';
import { Select } from '@arco-design/web-react';
import { IconLanguage } from '@arco-design/web-react/icon';
import Footer from '@/components/Footer';
import logoUrl from '@/assets/logo.svg';
import { GlobalContext } from '@/context';
import useLocale from '@/utils/useLocale';
import LoginForm from './form';
import LoginBanner from './banner';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
function Login() {
  const { siteName, siteIconUrl, lang, setLang } = useContext(GlobalContext);
  const t = useLocale();
  return (
    <div className={styles.container}>
      <div className={styles.logo}>
        <img src={siteIconUrl || logoUrl} alt={uiText('站点图标')} />
        <div className={styles['logo-text']}>{siteName || 'XiaoyuPostHub'}</div>
      </div>
      <Select
        value={lang}
        options={[
          { label: '中文', value: 'zh-CN' },
          { label: 'English', value: 'en-US' },
        ]}
        triggerElement={
          <button
            type="button"
            className={styles['language-switch']}
            aria-label={t['navbar.language']}
          >
            <IconLanguage />
            <span>{lang === 'en-US' ? 'English' : '中文'}</span>
          </button>
        }
        trigger="click"
        triggerProps={{
          autoAlignPopupWidth: false,
          autoAlignPopupMinWidth: true,
          position: 'br',
        }}
        onChange={(value) => setLang?.(value)}
      />
      <div className={styles.banner}>
        <div className={styles['banner-inner']}>
          <LoginBanner />
        </div>
      </div>
      <div className={styles.content}>
        <div className={styles['content-inner']}>
          <LoginForm />
        </div>
        <div className={styles.footer}>
          <Footer />
        </div>
      </div>
    </div>
  );
}
Login.displayName = 'LoginPage';
export default Login;
