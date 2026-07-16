import React from 'react';
import { IconFile, IconFolder } from '@arco-design/web-react/icon';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
export { formatBytes, formatTime } from '@/utils/format';
export interface ResourceItem {
  id: string;
  parentId?: string;
  kind: 'file' | 'folder';
  name: string;
  sizeBytes: number;
  mimeType?: string;
  reviewStatus?: 'approved' | 'pending' | 'rejected';
  reviewReason?: string;
  createdAt: string;
  updatedAt: string;
}
export function ResourceIcon({ kind }: { kind: ResourceItem['kind'] }) {
  return kind === 'folder' ? (
    <span className={`${styles['resource-icon']} ${styles.folder}`}>
      <IconFolder />
    </span>
  ) : (
    <span className={`${styles['resource-icon']} ${styles.file}`}>
      <IconFile />
    </span>
  );
}
export function linkStatus(item: {
  isActive: boolean;
  expiresAt?: string;
  reviewStatus?: string;
}) {
  if (item.reviewStatus === 'pending')
    return {
      text: uiText('待审核'),
      color: 'orange',
    };
  if (item.reviewStatus === 'rejected')
    return {
      text: uiText('已驳回'),
      color: 'red',
    };
  if (!item.isActive)
    return {
      text: uiText('已停用'),
      color: 'gray',
    };
  if (item.expiresAt && new Date(item.expiresAt).getTime() <= Date.now())
    return {
      text: uiText('已过期'),
      color: 'orange',
    };
  return {
    text: uiText('生效中'),
    color: 'green',
  };
}
