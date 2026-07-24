import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import DOMPurify from 'dompurify';
import {
  Trigger,
  Badge,
  Spin,
  Button,
  Message,
  Modal,
  Tag,
  Typography,
  Pagination,
  Popconfirm,
} from '@arco-design/web-react';
import { useHistory } from 'react-router-dom';
import useLocale from '../../utils/useLocale';
import MessageList, { MessageListType } from './list';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
import { formatTime } from '@/utils/format';
import writeClipboard from '@/utils/clipboard';
import { IconDelete } from '@arco-design/web-react/icon';
interface BackendMessage {
  id: number;
  sentAt: string;
  title: string;
  content: string;
  tag: string;
  read: boolean;
}
const noopUnreadChange = () => undefined;

function sanitizeMessageHTML(content: string) {
  return DOMPurify.sanitize(content, {
    USE_PROFILES: {
      html: true,
    },
    ADD_ATTR: ['data-message-action', 'data-copy-text'],
  });
}

function getMessagePreview(content: string) {
  const documentContent = new DOMParser().parseFromString(
    content,
    'text/html'
  );
  documentContent
    .querySelectorAll('[data-message-action]')
    .forEach((action) => action.remove());
  return (documentContent.body.textContent || '')
    .replace(/\s+/g, ' ')
    .trim();
}

function normalizeMessage(item: BackendMessage): MessageListType[number] {
  const content = sanitizeMessageHTML(item.content || '');
  return {
    id: item.id,
    title: item.title || uiText('系统消息'),
    content,
    preview: getMessagePreview(content) || uiText('暂无消息正文'),
    time: formatTime(item.sentAt),
    status: item.read ? 1 : 0,
    tag: item.tag || uiText('系统'),
  };
}
export function MessageCenter({
  onUnreadChange,
  page = false,
}: {
  onUnreadChange?: (count: number) => void;
  page?: boolean;
}) {
  const t = useLocale();
  const pageSize = page ? 10 : 6;
  const [loading, setLoading] = useState(false);
  const [sourceData, setSourceData] = useState<MessageListType>([]);
  const [currentPage, setCurrentPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [unreadCount, setUnreadCount] = useState(0);
  const [activeMessage, setActiveMessage] =
    useState<MessageListType[number]>();
  const fetchSourceData = useCallback(
    (showLoading = true) => {
      showLoading && setLoading(true);
      axios
        .get('/api/messages', {
          params: {
            page: currentPage,
            pageSize,
          },
        })
        .then((res) => {
          const nextTotal = Number(res.data.total) || 0;
          const lastPage = Math.max(
            1,
            Math.ceil(nextTotal / pageSize)
          );
          if (currentPage > lastPage) {
            setCurrentPage(lastPage);
            return;
          }
          setSourceData((res.data.items || []).map(normalizeMessage));
          setTotal(nextTotal);
          const nextUnreadCount = res.data.unreadCount || 0;
          setUnreadCount(nextUnreadCount);
          (onUnreadChange || noopUnreadChange)(nextUnreadCount);
          window.dispatchEvent(
            new CustomEvent('xph-message-unread-change', {
              detail: nextUnreadCount,
            })
          );
        })
        .catch(() => Message.error(uiText('消息加载失败')))
        .finally(() => {
          showLoading && setLoading(false);
        });
    },
    [currentPage, onUnreadChange, pageSize]
  );
  async function readMessage(data: MessageListType) {
    const ids = data.map((item) => item.id);
    try {
      await axios.post('/api/messages/read', {
        ids: ids.map(Number),
      });
      setActiveMessage((current) =>
        current && ids.includes(current.id)
          ? {
              ...current,
              status: 1,
            }
          : current
      );
      fetchSourceData(false);
    } catch {
      Message.error(uiText('消息状态更新失败'));
    }
  }
  async function readAllMessages() {
    try {
      await axios.post('/api/messages/read', {
        all: true,
      });
      setActiveMessage((current) =>
        current
          ? {
              ...current,
              status: 1,
            }
          : current
      );
      fetchSourceData(false);
    } catch {
      Message.error(uiText('消息状态更新失败'));
    }
  }
  function clearMessages() {
    if (!total) return;
    axios
      .post('/api/messages/delete', {
        all: true,
      })
      .then(() => {
        setActiveMessage(undefined);
        if (currentPage !== 1) {
          setCurrentPage(1);
        } else {
          fetchSourceData();
        }
      })
      .catch(() => Message.error(uiText('消息清空失败')));
  }
  function deleteActiveMessage() {
    if (!activeMessage) return;
    axios
      .post('/api/messages/delete', {
        ids: [Number(activeMessage.id)],
      })
      .then(() => {
        setActiveMessage(undefined);
        if (sourceData.length === 1 && currentPage > 1) {
          setCurrentPage((value) => value - 1);
        } else {
          fetchSourceData(false);
        }
      })
      .catch(() => Message.error(uiText('消息删除失败')));
  }
  useEffect(() => {
    fetchSourceData();
  }, [fetchSourceData]);
  async function handleContentAction(
    event: React.MouseEvent<HTMLElement>
  ) {
    if (!(event.target instanceof Element)) return;
    const action = event.target.closest<HTMLElement>(
      '[data-message-action]'
    );
    if (!action) return;
    event.preventDefault();
    event.stopPropagation();
    if (action.dataset.messageAction !== 'copy') return;
    const copied = await writeClipboard(action.dataset.copyText || '');
    Message[copied ? 'success' : 'error'](
      uiText(copied ? '已复制' : '复制失败')
    );
  }
  return (
    <div
      className={
        page ? styles['message-page'] : styles['message-box']
      }
    >
      <Spin
        loading={loading}
        style={{
          display: 'block',
        }}
      >
        <div className={styles['message-toolbar']}>
          <div className={styles['message-toolbar-title']}>
            <Typography.Text bold>
              {page ? uiText('全部消息') : uiText('消息中心')}
            </Typography.Text>
            <Typography.Text type="secondary">
              {uiText('共')} {total} {uiText('条')}
            </Typography.Text>
            {unreadCount > 0 && (
              <Typography.Text
                className={styles['message-unread-count']}
              >
                {unreadCount} {uiText('条未读')}
              </Typography.Text>
            )}
          </div>
          <div className={styles['message-toolbar-actions']}>
            <Button
              size="small"
              type="text"
              disabled={!unreadCount}
              onClick={readAllMessages}
            >
              {t['message.allRead']}
            </Button>
            <Popconfirm
              title={uiText('确认清空全部消息？')}
              content={uiText('清空后消息将从你的列表中移除。')}
              okText={uiText('清空')}
              cancelText={uiText('取消')}
              onOk={clearMessages}
            >
              <Button
                type="text"
                size="small"
                status="danger"
                disabled={!total}
              >
                {t['message.empty']}
              </Button>
            </Popconfirm>
          </div>
        </div>
        <div className={styles['message-list-scroll']}>
          <MessageList
            data={sourceData}
            onItemClick={setActiveMessage}
          />
        </div>
        <div className={styles['message-pagination']}>
          <Pagination
            simple
            size="small"
            showJumper={false}
            hideOnSinglePage
            current={currentPage}
            pageSize={pageSize}
            total={total}
            onChange={(nextPage) => {
              setActiveMessage(undefined);
              setCurrentPage(nextPage);
            }}
          />
        </div>
      </Spin>
      <Modal
        className={styles['message-detail-modal']}
        visible={Boolean(activeMessage)}
        title={uiText('消息详情')}
        footer={
          <div className={styles['message-detail-footer']}>
            <Button
              status="danger"
              type="text"
              icon={<IconDelete />}
              onClick={deleteActiveMessage}
            >
              {uiText('删除')}
            </Button>
            <div className={styles['message-detail-footer-actions']}>
              <Button onClick={() => setActiveMessage(undefined)}>
                {uiText('关闭')}
              </Button>
              {!activeMessage?.status && (
                <Button
                  type="primary"
                  onClick={() =>
                    activeMessage && readMessage([activeMessage])
                  }
                >
                  {uiText('标记为已读')}
                </Button>
              )}
            </div>
          </div>
        }
        maskClosable={false}
        unmountOnExit
        onCancel={() => setActiveMessage(undefined)}
        style={{
          width: 'min(620px, calc(100vw - 24px))',
        }}
      >
        {activeMessage && (
          <article className={styles['message-detail']}>
            <div className={styles['message-detail-heading']}>
              <Typography.Title heading={5}>
                {activeMessage.title}
              </Typography.Title>
              <Tag color="arcoblue">{activeMessage.tag}</Tag>
            </div>
            <Typography.Text
              className={styles['message-detail-time']}
              type="secondary"
            >
              {activeMessage.time}
            </Typography.Text>
            <div
              className={styles['message-detail-content']}
              onClick={handleContentAction}
              dangerouslySetInnerHTML={{
                __html:
                  activeMessage.content || '<p>暂无消息正文</p>',
              }}
            />
          </article>
        )}
      </Modal>
    </div>
  );
}
function MessageBox({
  children,
  mobile = false,
  onNavigate,
}: {
  children: React.ReactNode;
  mobile?: boolean;
  onNavigate?: () => void;
}) {
  const history = useHistory();
  const [unreadCount, setUnreadCount] = useState(0);
  useEffect(() => {
    axios
      .get('/api/messages?page=1&pageSize=1')
      .then((res) => setUnreadCount(res.data.unreadCount || 0))
      .catch(() => setUnreadCount(0));
    const handleUnreadChange = (event: Event) =>
      setUnreadCount(Number((event as CustomEvent<number>).detail) || 0);
    window.addEventListener(
      'xph-message-unread-change',
      handleUnreadChange
    );
    return () =>
      window.removeEventListener(
        'xph-message-unread-change',
        handleUnreadChange
      );
  }, []);
  const badge = (
    <Badge count={unreadCount} maxCount={99}>
      {children}
    </Badge>
  );
  if (!mobile) {
    return (
      <Trigger
        trigger="click"
        popup={() => (
          <MessageCenter onUnreadChange={setUnreadCount} />
        )}
        position="br"
        unmountOnExit
        popupAlign={{
          bottom: 4,
        }}
      >
        {badge}
      </Trigger>
    );
  }
  return (
    <span
      className={styles['message-trigger']}
      onClick={() => {
        history.push(
          history.location.pathname.startsWith('/admin')
            ? '/admin/messages'
            : '/messages'
        );
        onNavigate?.();
      }}
    >
      {badge}
    </span>
  );
}
export default MessageBox;
