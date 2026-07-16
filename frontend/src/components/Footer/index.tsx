import React, { useContext } from 'react';
import { Layout } from '@arco-design/web-react';
import { FooterProps } from '@arco-design/web-react/es/Layout/interface';
import cs from 'classnames';
import { GlobalContext } from '@/context';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
function Footer(props: FooterProps = {}) {
  const { siteName } = useContext(GlobalContext);
  const { className, ...restProps } = props;
  return (
    <Layout.Footer className={cs(styles.footer, className)} {...restProps}>
      {siteName || 'XiaoyuPostHub'}
      {uiText('· 文件与资源协作平台')}
    </Layout.Footer>
  );
}
export default Footer;
