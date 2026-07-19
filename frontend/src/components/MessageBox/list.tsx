import React, { useState } from 'react';
import {
  List,
  Avatar,
  Typography,
  Button,
  Space,
  Result,
  Tag,
} from '@arco-design/web-react';
import { IconDelete } from '@arco-design/web-react/icon';
import uiText from '@/utils/uiText';
import useLocale from '../../utils/useLocale';
import styles from './style/index.module.less';

export interface MessageItemData {
  id: string | number;
  type: string;
  title: string;
  subTitle?: string;
  avatar?: string;
  content: string;
  codes?: string[];
  time?: string;
  status: number;
  tag?: {
    text?: string;
    color?: string;
  };
}

export type MessageListType = MessageItemData[];

interface MessageListProps {
  data: MessageItemData[];
  unReadData: MessageItemData[];
  onItemClick?: (item: MessageItemData, index: number) => void;
  onItemDelete?: (item: MessageItemData, index: number) => void;
  onAllBtnClick?: (
    unReadData: MessageItemData[],
    data: MessageItemData[]
  ) => void;
}

function MessageList(props: MessageListProps) {
  const t = useLocale();
  const { data, unReadData } = props;
  const [expandedId, setExpandedId] = useState<string | number>();

  function onItemClick(item: MessageItemData, index: number) {
    setExpandedId((current) => (current === item.id ? undefined : item.id));
    props.onItemClick && props.onItemClick(item, index);
  }

  function onAllBtnClick() {
    props.onAllBtnClick && props.onAllBtnClick(unReadData, data);
  }

  return (
    <List
      noDataElement={<Result status="404" subTitle={t['message.empty.tips']} />}
      footer={
        <div className={styles.footer}>
          <div className={styles['footer-item']}>
            <Button
              type="text"
              size="small"
              disabled={!unReadData.length}
              onClick={onAllBtnClick}
            >
              {t['message.allRead']}
            </Button>
          </div>
        </div>
      }
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
            style={{
              cursor: 'pointer',
            }}
            onClick={() => {
              onItemClick(item, index);
            }}
          >
            <List.Item.Meta
              avatar={
                item.avatar && (
                  <Avatar shape="circle" size={36}>
                    <img src={item.avatar} />
                  </Avatar>
                )
              }
              title={
                <div className={styles['message-title']}>
                  <Space size={4}>
                    <span>{item.title}</span>
                    <Typography.Text type="secondary">
                      {item.subTitle}
                    </Typography.Text>
                  </Space>
                  {item.tag && item.tag.text ? (
                    <Tag color={item.tag.color}>{item.tag.text}</Tag>
                  ) : null}
                  <Button
                    className={styles['delete-button']}
                    type="text"
                    size="mini"
                    status="danger"
                    icon={<IconDelete />}
                    aria-label={uiText('删除消息')}
                    title={uiText('删除消息')}
                    onClick={(event) => {
                      event.stopPropagation();
                      props.onItemDelete?.(item, index);
                    }}
                  />
                </div>
              }
              description={
                <div>
                  <Typography.Paragraph
                    className={styles['message-content']}
                    style={{ marginBottom: 0 }}
                    ellipsis={expandedId !== item.id}
                  >
                    {item.content}
                  </Typography.Paragraph>
                  {item.codes && item.codes.length > 0 && (
                    <div className={styles['message-codes']}>
                      {item.codes.map((code) => (
                        <Typography.Text
                          key={code}
                          code
                          copyable={{ text: code }}
                        >
                          {code}
                        </Typography.Text>
                      ))}
                    </div>
                  )}
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
