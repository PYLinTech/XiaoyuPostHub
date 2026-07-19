import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import {
  Trigger,
  Badge,
  Tabs,
  Spin,
  Button,
  Message,
} from '@arco-design/web-react';
import useLocale from '../../utils/useLocale';
import MessageList, { MessageListType } from './list';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
import { formatTime } from '@/utils/format';
interface BackendMessage {
  id: number;
  sentAt: string;
  messageType: string;
  content?: {
    title?: string;
    body?: string;
    codes?: string[];
  };
  read: boolean;
}
function normalizeMessage(item: BackendMessage): MessageListType[number] {
  const content = item.content || {};
  return {
    id: item.id,
    type: item.messageType === 'message' ? 'message' : 'notice',
    title: content.title || uiText('系统消息'),
    content: content.body || '',
    codes: Array.isArray(content.codes) ? content.codes : [],
    time: formatTime(item.sentAt),
    status: item.read ? 1 : 0,
    tag:
      item.messageType === 'invitation'
        ? {
            text: uiText('邀请码'),
            color: 'arcoblue',
          }
        : undefined,
  };
}
function DropContent({
  onUnreadChange,
}: {
  onUnreadChange: (count: number) => void;
}) {
  const t = useLocale();
  const [loading, setLoading] = useState(false);
  const [sourceData, setSourceData] = useState<MessageListType>([]);
  const fetchSourceData = useCallback(
    (showLoading = true) => {
      showLoading && setLoading(true);
      axios
        .get('/api/messages')
        .then((res) => {
          setSourceData((res.data.items || []).map(normalizeMessage));
          onUnreadChange(res.data.unreadCount || 0);
        })
        .catch(() => Message.error(uiText('消息加载失败')))
        .finally(() => {
          showLoading && setLoading(false);
        });
    },
    [onUnreadChange]
  );
  function readMessage(data: MessageListType) {
    const ids = data.map((item) => item.id);
    axios
      .post('/api/messages/read', {
        ids: ids.map(Number),
      })
      .then(() => {
        fetchSourceData();
      })
      .catch(() => Message.error(uiText('消息状态更新失败')));
  }
  function clearMessages() {
    if (!sourceData.length) return;
    axios
      .post('/api/messages/delete', {
        ids: sourceData.map((item) => Number(item.id)),
      })
      .then(() => fetchSourceData())
      .catch(() => Message.error(uiText('消息清空失败')));
  }
  function deleteMessage(id: string | number) {
    axios
      .post('/api/messages/delete', {
        ids: [Number(id)],
      })
      .then(() => fetchSourceData(false))
      .catch(() => Message.error(uiText('消息删除失败')));
  }
  useEffect(() => {
    fetchSourceData();
  }, [fetchSourceData]);
  const groupData = sourceData.reduce<{
    [key: string]: MessageListType;
  }>((groups, item) => {
    (groups[item.type] ||= []).push(item);
    return groups;
  }, {});
  const tabList = [
    {
      key: 'message',
      title: t['message.tab.title.message'],
    },
    {
      key: 'notice',
      title: t['message.tab.title.notice'],
    },
  ];
  return (
    <div className={styles['message-box']}>
      <Spin
        loading={loading}
        style={{
          display: 'block',
        }}
      >
        <Tabs
          overflow="dropdown"
          type="rounded"
          defaultActiveTab="notice"
          destroyOnHide
          extra={
            <Button type="text" onClick={clearMessages}>
              {t['message.empty']}
            </Button>
          }
        >
          {tabList.map((item) => {
            const { key, title } = item;
            const data = groupData[key] || [];
            const unReadData = data.filter((item) => !item.status);
            return (
              <Tabs.TabPane
                key={key}
                title={
                  <span>
                    {title}
                    {unReadData.length ? `(${unReadData.length})` : ''}
                  </span>
                }
              >
                <div className={styles['message-list-scroll']}>
                  <MessageList
                    data={data}
                    unReadData={unReadData}
                    onItemClick={(item) => {
                      if (!item.status) readMessage([item]);
                    }}
                    onItemDelete={(item) => deleteMessage(item.id)}
                    onAllBtnClick={(unReadData) => {
                      readMessage(unReadData);
                    }}
                  />
                </div>
              </Tabs.TabPane>
            );
          })}
        </Tabs>
      </Spin>
    </div>
  );
}
function MessageBox({
  children,
  position = 'br',
}: {
  children: React.ReactNode;
  position?: 'br' | 'rt';
}) {
  const [unreadCount, setUnreadCount] = useState(0);
  useEffect(() => {
    axios
      .get('/api/messages?limit=1')
      .then((res) => setUnreadCount(res.data.unreadCount || 0))
      .catch(() => setUnreadCount(0));
  }, []);
  return (
    <Trigger
      trigger="click"
      popup={() => <DropContent onUnreadChange={setUnreadCount} />}
      position={position}
      unmountOnExit
      popupAlign={{
        bottom: 4,
      }}
    >
      <Badge count={unreadCount} maxCount={99}>
        {children}
      </Badge>
    </Trigger>
  );
}
export default MessageBox;
