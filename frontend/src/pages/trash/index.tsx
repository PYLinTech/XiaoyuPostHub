import React, { useCallback, useContext, useEffect, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Card,
  Message,
  Modal,
  Space,
  Table,
  Typography,
} from '@arco-design/web-react';
import { IconDelete, IconRefresh } from '@arco-design/web-react/icon';
import { GlobalContext } from '@/context';
import uiText from '@/utils/uiText';
import {
  formatBytes,
  formatTime,
  ResourceIcon,
  ResourceItem,
} from '../storage/shared';
import styles from '../storage/style/index.module.less';

function approximateExpiry(
  trashedAt: string | undefined,
  retentionDays: number
) {
  if (!trashedAt) return '-';
  const expiresAt = new Date(trashedAt);
  expiresAt.setDate(expiresAt.getDate() + retentionDays);
  return `${uiText('约')} ${expiresAt.toLocaleDateString()}`;
}

export default function TrashPage() {
  const { userInfo } = useContext(GlobalContext);
  const canDelete = (userInfo?.permissions || []).includes('delete_own');
  const [items, setItems] = useState<ResourceItem[]>([]);
  const [retentionDays, setRetentionDays] = useState(30);
  const [loading, setLoading] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    return axios
      .get('/api/trash')
      .then((response) => {
        setItems(response.data.items || []);
        setRetentionDays(response.data.retentionDays || 30);
      })
      .catch((error) =>
        Message.error(error?.response?.data?.msg || uiText('回收站加载失败'))
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const restore = async (item: ResourceItem) => {
    try {
      await axios.post(`/api/trash/${item.id}/restore`);
      Message.success(uiText('恢复完成'));
      await load();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('恢复失败'));
    }
  };

  const permanentlyDelete = (item: ResourceItem) => {
    Modal.confirm({
      title: uiText('永久删除'),
      content: uiText('永久删除后无法恢复。'),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await axios.delete(`/api/trash/${item.id}`);
          Message.success(uiText('永久删除完成'));
          await load();
        } catch (error) {
          Message.error(error?.response?.data?.msg || uiText('永久删除失败'));
        }
      },
    });
  };

  const emptyTrash = () => {
    Modal.confirm({
      title: uiText('清空回收站'),
      content: uiText('回收站中的全部内容将被永久删除且无法恢复。'),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await axios.delete('/api/trash');
          Message.success(uiText('回收站已清空'));
          await load();
        } catch (error) {
          Message.error(error?.response?.data?.msg || uiText('清空回收站失败'));
        }
      },
    });
  };

  const columns = [
    {
      title: uiText('名称'),
      dataIndex: 'name',
      render: (_, item: ResourceItem) => (
        <div className={styles['resource-name']}>
          <ResourceIcon kind={item.kind} />
          <Typography.Text ellipsis>{item.name}</Typography.Text>
        </div>
      ),
    },
    {
      title: uiText('大小'),
      dataIndex: 'sizeBytes',
      width: 140,
      className: styles['mobile-hidden'],
      render: (value, item: ResourceItem) =>
        item.kind === 'folder' ? '-' : formatBytes(value),
    },
    {
      title: uiText('删除时间'),
      dataIndex: 'trashedAt',
      width: 210,
      className: styles['mobile-hidden'],
      render: formatTime,
    },
    {
      title: uiText('到期时间'),
      dataIndex: 'trashedAt',
      width: 160,
      className: styles['mobile-hidden'],
      render: (value) => approximateExpiry(value, retentionDays),
    },
    {
      title: uiText('操作'),
      width: 190,
      render: (_, item: ResourceItem) => (
        <Space>
          <Button
            size="small"
            disabled={!canDelete || item.restoreBlocked}
            onClick={() => restore(item)}
          >
            {item.restoreBlocked ? uiText('管理员限制恢复') : uiText('恢复')}
          </Button>
          <Button
            size="small"
            status="danger"
            disabled={!canDelete}
            onClick={() => permanentlyDelete(item)}
          >
            {uiText('永久删除')}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <Typography.Title heading={4} className={styles.title}>
            {uiText('回收站')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {uiText('回收站最多为您保留')} {retentionDays} {uiText('天')}
            {uiText('，到期前您可以还原或提前删除。')}
          </Typography.Text>
        </div>
      </div>
      <Card className={styles.workspace}>
        <div className={styles.toolbar}>
          <div className={styles['toolbar-left']}>
            <Button icon={<IconRefresh />} onClick={load} loading={loading}>
              {uiText('刷新数据')}
            </Button>
            <Button
              status="danger"
              icon={<IconDelete />}
              disabled={!canDelete || !items.length}
              onClick={emptyTrash}
            >
              {uiText('清空回收站')}
            </Button>
          </div>
          <Typography.Text type="secondary">
            {uiText('共')} {items.length} {uiText('项')}
          </Typography.Text>
        </div>
        <Table
          rowKey="id"
          loading={loading}
          columns={columns}
          data={items}
          pagination={false}
          noDataElement={uiText('回收站为空')}
        />
      </Card>
    </div>
  );
}
