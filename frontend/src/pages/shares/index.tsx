import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Card,
  Message,
  Modal,
  Table,
  Tag,
  Tooltip,
  Typography,
} from '@arco-design/web-react';
import {
  IconCopy,
  IconDelete,
  IconEdit,
  IconPause,
  IconPlayArrow,
} from '@arco-design/web-react/icon';
import LinkConfigModal from '../storage/link-config-modal';
import {
  formatBytes,
  formatTime,
  linkStatus,
  ResourceIcon,
  ResourceItem,
} from '../storage/shared';
import styles from '../storage/style/index.module.less';
import uiText from '@/utils/uiText';
import writeClipboard from '@/utils/clipboard';
interface ShareItem {
  id: number;
  url?: string;
  shareType?: 'link' | 'pickup';
  pickupCode?: string;
  password?: string;
  resource: ResourceItem;
  expiresAt?: string;
  hasPassword: boolean;
  showOwner: boolean;
  description: string;
  descriptionFormat: 'markdown' | 'html';
  downloadLimit?: number;
  trafficLimitBytes?: number;
  downloadCount: number;
  trafficUsedBytes: number;
  isActive: boolean;
  reviewStatus?: 'approved' | 'pending' | 'rejected';
  reviewReason?: string;
  createdAt: string;
}
export default function SharesPage() {
  const [items, setItems] = useState<ShareItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [editing, setEditing] = useState<ShareItem>();
  const [operating, setOperating] = useState(false);
  const load = useCallback(() => {
    setLoading(true);
    return axios
      .get('/api/shares')
      .then((response) => setItems(response.data.items || []))
      .catch((error) =>
        Message.error(error?.response?.data?.msg || uiText('分享列表加载失败'))
      )
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => {
    load();
  }, [load]);
  const batch = async (action: 'enable' | 'disable' | 'delete') => {
    if (!selectedKeys.length) return;
    setOperating(true);
    try {
      await axios.post('/api/shares/manage', {
        ids: selectedKeys,
        action,
      });
      setSelectedKeys([]);
      Message.success(
        action === 'delete'
          ? uiText('分享已删除')
          : action === 'enable'
          ? uiText('分享已启用')
          : uiText('分享已禁用')
      );
      await load();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('批量操作失败'));
    } finally {
      setOperating(false);
    }
  };
  const confirmDelete = () => {
    if (!selectedKeys.length) return;
    Modal.confirm({
      title: `${uiText('删除分享')}：${selectedKeys.length}`,
      content: uiText('分享链接将立即失效且无法恢复，原文件不会被删除。'),
      okButtonProps: {
        status: 'danger',
      },
      onOk: () => batch('delete'),
    });
  };
  const copyValue = async (value: string) => {
    const copied = await writeClipboard(value);
    Message[copied ? 'success' : 'error'](
      uiText(copied ? '已复制' : '复制失败')
    );
  };
  const columns = [
    {
      title: uiText('分享内容'),
      render: (_, item: ShareItem) => {
        const status = linkStatus(item);
        const isPickup = item.shareType === 'pickup' && item.pickupCode;
        const shareValue =
          isPickup
            ? item.pickupCode
            : item.url
            ? new URL(item.url, window.location.origin).toString()
            : uiText('旧记录不可恢复');
        return (
          <div className={styles['resource-name']}>
            <ResourceIcon kind={item.resource.kind} />
            <div className={styles['resource-main']}>
              <span className={styles['share-resource-title']}>
                {item.resource.name}
              </span>
              <div className={styles['share-mobile-meta']}>
                <div className={styles['share-mobile-value']}>
                  <span className={styles['share-mobile-label']}>
                    {uiText(isPickup ? '取件码：' : '分享链接：')}
                  </span>
                  <span className={styles['share-mobile-code']}>
                    <code title={shareValue}>{shareValue}</code>
                    {(item.pickupCode || item.url) && (
                      <Tooltip content={uiText('复制')}>
                        <Button
                          className={styles['share-copy-button']}
                          size="small"
                          type="text"
                          icon={<IconCopy />}
                          aria-label={uiText('复制')}
                          onClick={() =>
                            copyValue(
                              isPickup
                                ? (item.pickupCode as string)
                                : shareValue
                            )
                          }
                        />
                      </Tooltip>
                    )}
                  </span>
                </div>
                <div className={styles['share-mobile-details']}>
                  <span>
                    {uiText('状态')}：{status.text}
                  </span>
                  <span>
                    {uiText('访问密码')}：
                    {item.hasPassword
                      ? item.password || uiText('旧记录不可恢复')
                      : uiText('无密码')}
                  </span>
                  <span>
                    {uiText('下载次数')}：{item.downloadCount} /{' '}
                    {item.downloadLimit ?? uiText('不限')}
                  </span>
                  <span>
                    {uiText('下载流量')}：
                    {formatBytes(item.trafficUsedBytes)} /{' '}
                    {item.trafficLimitBytes == null
                      ? uiText('不限')
                      : formatBytes(item.trafficLimitBytes)}
                  </span>
                  <span>
                    {uiText('有效期至')}：
                    {item.expiresAt
                      ? formatTime(item.expiresAt)
                      : uiText('永久')}
                  </span>
                  <span>
                    {uiText('创建时间')}：{formatTime(item.createdAt)}
                  </span>
                </div>
                <Button
                  className={styles['share-mobile-config']}
                  type="text"
                  size="small"
                  icon={<IconEdit />}
                  onClick={() => setEditing(item)}
                >
                  {uiText('配置')}
                </Button>
              </div>
            </div>
          </div>
        );
      },
    },
    {
      title: uiText('分享方式'),
      width: 360,
      className: styles['mobile-hidden'],
      render: (_, item: ShareItem) => {
        if (item.shareType === 'pickup' && item.pickupCode) {
          return <div className={styles['link-cell']}><code>{uiText('取件码')}：{item.pickupCode}</code><Button size="mini" type="text" icon={<IconCopy />} onClick={() => copyValue(item.pickupCode as string)} /></div>;
        }
        if (!item.url)
          return (
            <Typography.Text type="secondary">
              {uiText('旧记录不可恢复')}
            </Typography.Text>
          );
        const url = new URL(item.url, window.location.origin).toString();
        return (
          <div className={styles['link-cell']}>
            <a href={url} target="_blank" rel="noreferrer" title={url}>
              {url}
            </a>
            <Tooltip content={uiText('复制链接')}>
              <Button
                size="mini"
                type="text"
                icon={<IconCopy />}
                onClick={() => copyValue(url)}
              />
            </Tooltip>
          </div>
        );
      },
    },
    {
      title: uiText('状态'),
      width: 110,
      className: styles['mobile-hidden'],
      render: (_, item) => {
        const status = linkStatus(item);
        const tag = <Tag color={status.color}>{status.text}</Tag>;
        return item.reviewReason ? (
          <Tooltip content={item.reviewReason}>{tag}</Tooltip>
        ) : (
          tag
        );
      },
    },
    {
      title: uiText('访问密码'),
      width: 190,
      className: styles['mobile-hidden'],
      render: (_, item: ShareItem) => {
        if (!item.hasPassword) return uiText('无密码');
        if (!item.password)
          return (
            <Typography.Text type="secondary">
              {uiText('旧记录不可恢复')}
            </Typography.Text>
          );
        return (
          <div className={styles['secret-cell']}>
            <code title={item.password}>{item.password}</code>
            <Tooltip content={uiText('复制密码')}>
              <Button
                size="mini"
                type="text"
                icon={<IconCopy />}
                onClick={() => copyValue(item.password as string)}
              />
            </Tooltip>
          </div>
        );
      },
    },
    {
      title: uiText('下载次数'),
      width: 140,
      className: styles['mobile-hidden'],
      render: (_, item) =>
        `${item.downloadCount} / ${item.downloadLimit ?? uiText('不限')}`,
    },
    {
      title: uiText('下载流量'),
      width: 180,
      className: styles['mobile-hidden'],
      render: (_, item) =>
        `${formatBytes(item.trafficUsedBytes)} / ${
          item.trafficLimitBytes == null
            ? uiText('不限')
            : formatBytes(item.trafficLimitBytes)
        }`,
    },
    {
      title: uiText('有效期至'),
      width: 210,
      className: styles['mobile-hidden'],
      dataIndex: 'expiresAt',
      render: (value) => (value ? formatTime(value) : uiText('永久')),
    },
    {
      title: uiText('创建时间'),
      width: 210,
      className: styles['mobile-hidden'],
      dataIndex: 'createdAt',
      render: formatTime,
    },
    {
      title: uiText('操作'),
      width: 90,
      fixed: 'right' as const,
      className: styles['mobile-hidden'],
      render: (_, item: ShareItem) => (
        <Button
          type="text"
          size="small"
          icon={<IconEdit />}
          onClick={() => setEditing(item)}
        >
          {uiText('配置')}
        </Button>
      ),
    },
  ];
  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <Typography.Title heading={4} className={styles.title}>
            {uiText('分享')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {uiText('查看你已经生成的文件和文件夹分享。')}
          </Typography.Text>
        </div>
      </div>
      <Card className={styles['list-card']}>
        <div className={styles.toolbar}>
          <div className={styles['toolbar-left']}>
            <Button
              icon={<IconPlayArrow />}
              disabled={!selectedKeys.length}
              loading={operating}
              onClick={() => batch('enable')}
            >
              {uiText('启用')}
            </Button>
            <Button
              icon={<IconPause />}
              disabled={!selectedKeys.length}
              loading={operating}
              onClick={() => batch('disable')}
            >
              {uiText('禁用')}
            </Button>
            <Button
              status="danger"
              icon={<IconDelete />}
              disabled={!selectedKeys.length}
              loading={operating}
              onClick={confirmDelete}
            >
              {uiText('删除')}
            </Button>
          </div>
          <Typography.Text type="secondary">
            {selectedKeys.length
              ? `${uiText('已选择')} ${selectedKeys.length} ${uiText('项')}`
              : `${uiText('共')} ${items.length} ${uiText('项')}`}
          </Typography.Text>
        </div>
        <Table
          className={styles['share-table']}
          rowKey="id"
          loading={loading}
          columns={columns}
          data={items}
          pagination={{
            pageSize: 15,
            showTotal: true,
          }}
          scroll={{
            x: 1740,
          }}
          rowSelection={{
            type: 'checkbox',
            selectedRowKeys: selectedKeys,
            onChange: (keys) => setSelectedKeys(keys.map(Number)),
          }}
          noDataElement={uiText('暂无分享')}
        />
      </Card>
      <LinkConfigModal
        mode="share"
        item={editing}
        visible={Boolean(editing)}
        onClose={() => setEditing(undefined)}
        onSaved={load}
      />
    </div>
  );
}
