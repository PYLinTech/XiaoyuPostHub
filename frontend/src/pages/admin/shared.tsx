import React from 'react';
import { Typography } from '@arco-design/web-react';
import styles from './style/index.module.less';

export { formatBytes, formatLimit } from '@/utils/format';

const { Title, Text } = Typography;

export function AdminPageHeader({ title, description, extra = null }) {
  return (
    <div className={styles.header}>
      <div>
        <Title heading={4} className={styles.title}>
          {title}
        </Title>
        <Text type="secondary">{description}</Text>
      </div>
      {extra}
    </div>
  );
}
