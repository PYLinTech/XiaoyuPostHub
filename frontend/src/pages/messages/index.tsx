import React from 'react';
import { Card, Typography } from '@arco-design/web-react';
import { MessageCenter } from '@/components/MessageBox';
import uiText from '@/utils/uiText';
import styles from './index.module.less';

export default function MessagesPage() {
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <Typography.Title heading={4}>
          {uiText('消息中心')}
        </Typography.Title>
        <Typography.Text type="secondary">
          {uiText('查看通知、系统消息和需要处理的内容。')}
        </Typography.Text>
      </header>
      <Card className={styles.card}>
        <MessageCenter page />
      </Card>
    </div>
  );
}
