import React, { useContext } from 'react';
import Footer from '@/components/Footer';
import logoUrl from '@/assets/logo.svg';
import { GlobalContext } from '@/context';
import LoginForm from './form';
import LoginBanner from './banner';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
function Login() {
  const { siteName, siteIconUrl } = useContext(GlobalContext);
  return (
    <div className={styles.container}>
      <div className={styles.logo}>
        <img src={siteIconUrl || logoUrl} alt={uiText('站点图标')} />
        <div className={styles['logo-text']}>{siteName || 'XiaoyuPostHub'}</div>
      </div>
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
