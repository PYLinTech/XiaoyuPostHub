import React from 'react';
import {
  List,
  Typography,
  Space,
  Result,
  Tag,
} from '@arco-design/web-react';
import uiText from '@/utils/uiText';
import useLocale from '../../utils/useLocale';
import styles from './style/index.module.less';

export interface MessageItemData {
  id: string | number;
  title: string;
  content: string;
  preview: string;
  time?: string;
  status: number;
  tag: string;
}

export type MessageListType = MessageItemData[];

interface MessageListProps {
  data: MessageItemData[];
  onItemClick?: (item: MessageItemData, index: number) => void;
}

function MessageList(props: MessageListProps) {
  const t = useLocale();
  const { data } = props;

  function onItemClick(item: MessageItemData, index: number) {
    props.onItemClick && props.onItemClick(item, index);
  }

  return (
    <List
      noDataElement={<Result status="404" subTitle={t['message.empty.tips']} />}
    >
      {data.map((item, index) => (
        <List.Item
          key={item.id}
          actionLayout="vertical"
          className={`${styles['message-item']} ${
            item.status ? styles.read : styles.unread
          }`}
        >
          <div
            className={styles['message-item-content']}
            role="button"
            tabIndex={0}
            onKeyDown={(event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                onItemClick(item, index);
              }
            }}
            onClick={() => {
              onItemClick(item, index);
            }}
          >
            <List.Item.Meta
              title={
                <div className={styles['message-title']}>
                  <Space size={6}>
                    {!item.status && (
                      <span
                        className={styles['unread-dot']}
                        aria-label={uiText('未读')}
                      />
                    )}
                    <span className={styles['message-title-text']}>
                      {item.title}
                    </span>
                  </Space>
                  <Tag color="arcoblue">{item.tag}</Tag>
                </div>
              }
              description={
                <div>
                  <div className={styles['message-content']}>
                    {item.preview}
                  </div>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {item.time}
                  </Typography.Text>
                </div>
              }
            />
          </div>
        </List.Item>
      ))}
    </List>
  );
}

export default MessageList;
