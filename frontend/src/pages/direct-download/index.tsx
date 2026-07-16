import React, { useContext, useState } from 'react';
import { Button, Card, Typography } from '@arco-design/web-react';
import { IconDownload } from '@arco-design/web-react/icon';
import { useParams } from 'react-router-dom';
import { GlobalContext } from '@/context';
import logoUrl from '@/assets/logo.svg';
import styles from '../share/style/index.module.less';
import uiText from '@/utils/uiText';
export default function PublicDirectDownloadPage() {
  const { token } = useParams<{
    token: string;
  }>();
  const { siteName, siteIconUrl } = useContext(GlobalContext);
  const [downloadAttempt, setDownloadAttempt] = useState(0);
  const downloadURL = `/api/direct/${encodeURIComponent(token)}`;
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <img src={siteIconUrl || logoUrl} alt={uiText('站点图标')} />
          <span>{siteName || 'XiaoyuPostHub'}</span>
        </div>
        <span className={styles['header-label']}>{uiText('文件下载')}</span>
      </header>
      <main className={styles.main}>
        <Card className={styles['download-card']}>
          <IconDownload className={styles['download-icon']} />
          <Typography.Title heading={5}>
            {uiText('下载已开始')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {uiText('如果浏览器没有自动下载，请点击下方按钮重试。')}
          </Typography.Text>
          <Button
            type="primary"
            size="large"
            icon={<IconDownload />}
            onClick={() => setDownloadAttempt((value) => value + 1)}
          >
            {uiText('重新下载')}
          </Button>
        </Card>
        <iframe
          key={downloadAttempt}
          title={uiText('直链下载')}
          src={downloadURL}
          hidden
        />
      </main>
      <footer className={styles.footer}>
        {siteName || 'XiaoyuPostHub'}
        {uiText('· 文件下载')}
      </footer>
    </div>
  );
}
