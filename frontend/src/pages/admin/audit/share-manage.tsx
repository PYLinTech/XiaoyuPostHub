import {
  deleteAdminShare,
  fetchAdminShares,
  releaseAdminPickupCodes,
  updateAdminShare,
} from '@/api/endpoints';
import { apiErrorMessage } from '@/api/client';
import React, { useCallback, useEffect, useState } from 'react';
import {
  Button,
  Input,
  Message,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from '@arco-design/web-react';
import { IconDelete, IconRefresh } from '@arco-design/web-react/icon';
import uiText from '@/utils/uiText';
import { formatBytes } from '../shared';

const formatTime = (value?: string) =>
  value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '-';

interface AdminShareItem {
  id: number;
  ownerUserId: number;
  ownerName: string;
  shareType: 'link' | 'pickup';
  url?: string;
  pickupCode?: string;
  pickupCodeLive: boolean;
  expiresAt?: string;
  isActive: boolean;
  adminBlocked: boolean;
  deleted: boolean;
  downloadCount: number;
  trafficUsedBytes: number;
  resourceName: string;
  createdAt: string;
}

// 改期选项：0 = 永久（后端按"允许永久取件码"开关校验）。
const expiryChoices = () => [
  { label: uiText('保持当前'), value: 'keep' },
  { label: uiText('从现在起 1 天'), value: '86400' },
  { label: uiText('从现在起 7 天'), value: '604800' },
  { label: uiText('从现在起 30 天'), value: '2592000' },
  { label: uiText('永久有效'), value: '0' },
];

/**
 * 管理端"分享与取件码"管理：管理员可随时改期（含永久）、启停、删除，
 * 并一键释放失效取件码腾出码空间。
 */
export default function ShareManage() {
  const [items, setItems] = useState<AdminShareItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [operating, setOperating] = useState(false);
  const [shareType, setShareType] = useState('pickup');
  const [keyword, setKeyword] = useState('');

  const load = useCallback(() => {
    setLoading(true);
    return fetchAdminShares({
      ...(shareType === 'all' ? {} : { type: shareType }),
      ...(keyword ? { q: keyword } : {}),
    })
      .then((response) => setItems(response.data.items || []))
      .catch((error) =>
        Message.error(apiErrorMessage(error, uiText('分享列表加载失败')))
      )
      .finally(() => setLoading(false));
  }, [keyword, shareType]);

  useEffect(() => {
    load();
  }, [load]);

  const changeExpiry = async (item: AdminShareItem, value: string) => {
    if (value === 'keep') return;
    setOperating(true);
    try {
      const response = await updateAdminShare(item.id, {
        expiresInSeconds: Number(value),
      });
      if (response.data?.pickupCodeRotated && response.data?.pickupCode) {
        Message.warning(
          `${uiText('部分取件码已更换')}：${response.data.pickupCode}`
        );
      } else {
        Message.success(uiText('配置已保存'));
      }
      await load();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('保存配置失败')));
    } finally {
      setOperating(false);
    }
  };

  const toggleActive = async (item: AdminShareItem) => {
    setOperating(true);
    try {
      const response = await updateAdminShare(item.id, {
        active: !item.isActive,
      });
      if (response.data?.pickupCodeRotated && response.data?.pickupCode) {
        Message.warning(
          `${uiText('部分取件码已更换')}：${response.data.pickupCode}`
        );
      }
      await load();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('操作失败')));
    } finally {
      setOperating(false);
    }
  };

  const unblock = async (item: AdminShareItem) => {
    setOperating(true);
    try {
      await updateAdminShare(item.id, { unblock: true, active: true });
      Message.success(uiText('操作成功'));
      await load();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('操作失败')));
    } finally {
      setOperating(false);
    }
  };

  const remove = (item: AdminShareItem) => {
    Modal.confirm({
      title: uiText('删除分享'),
      content: uiText('分享链接将立即失效且无法恢复，原文件不会被删除。'),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await deleteAdminShare(item.id);
          Message.success(uiText('分享已删除'));
          await load();
        } catch (error) {
          Message.error(apiErrorMessage(error, uiText('删除失败')));
        }
      },
    });
  };

  const releaseCodes = async () => {
    setOperating(true);
    try {
      const response = await releaseAdminPickupCodes();
      const released = Number(response.data?.released || 0);
      if (released > 0) {
        Message.success(`${uiText('已释放失效取件码')}：${released}`);
      } else {
        Message.info(uiText('没有需要清理的失效取件码'));
      }
      await load();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('清理失效取件码失败')));
    } finally {
      setOperating(false);
    }
  };

  const columns = [
    {
      title: uiText('所有者'),
      dataIndex: 'ownerName',
      width: 140,
    },
    {
      title: uiText('分享内容'),
      dataIndex: 'resourceName',
      width: 180,
      render: (value: string) => value || '-',
    },
    {
      title: uiText('分享方式'),
      width: 220,
      render: (_: unknown, item: AdminShareItem) => {
        if (item.shareType === 'pickup' && item.pickupCode) {
          return (
            <Space size={4}>
              <code>{item.pickupCode}</code>
              {!item.pickupCodeLive && <Tag color="gray">{uiText('码已失效')}</Tag>}
            </Space>
          );
        }
        return <Typography.Text type="secondary">{item.url || '-'}</Typography.Text>;
      },
    },
    {
      title: uiText('状态'),
      width: 150,
      render: (_: unknown, item: AdminShareItem) => (
        <Space size={4}>
          {item.adminBlocked && <Tag color="red">{uiText('已封禁')}</Tag>}
          {item.deleted && <Tag color="gray">{uiText('已删除')}</Tag>}
          {!item.deleted && (
            <Tag color={item.isActive ? 'green' : 'gray'}>
              {uiText(item.isActive ? '已启用' : '已停用')}
            </Tag>
          )}
          {item.expiresAt ? (
            <Tag color="orange">{uiText('到期时间')}</Tag>
          ) : (
            <Tag color="arcoblue">{uiText('永久有效')}</Tag>
          )}
        </Space>
      ),
    },
    {
      title: uiText('到期时间'),
      dataIndex: 'expiresAt',
      width: 180,
      render: (value?: string) => (value ? formatTime(value) : uiText('永久有效')),
    },
    {
      title: uiText('下载'),
      width: 150,
      render: (_: unknown, item: AdminShareItem) =>
        `${item.downloadCount} · ${formatBytes(item.trafficUsedBytes)}`,
    },
    {
      title: uiText('创建时间'),
      dataIndex: 'createdAt',
      width: 180,
      render: (value: string) => formatTime(value),
    },
    {
      title: uiText('操作'),
      width: 290,
      fixed: 'right' as const,
      render: (_: unknown, item: AdminShareItem) => (
        <Space size={4}>
          <Select
            size="mini"
            value="keep"
            options={expiryChoices()}
            disabled={operating || item.deleted}
            onChange={(value) => changeExpiry(item, String(value))}
            style={{ width: 130 }}
          />
          {!item.deleted && (
            <Button size="mini" disabled={operating} onClick={() => toggleActive(item)}>
              {uiText(item.isActive ? '停用' : '启用')}
            </Button>
          )}
          {item.adminBlocked && (
            <Button size="mini" disabled={operating} onClick={() => unblock(item)}>
              {uiText('解除封禁')}
            </Button>
          )}
          {!item.deleted && (
            <Button
              size="mini"
              status="danger"
              icon={<IconDelete />}
              disabled={operating}
              onClick={() => remove(item)}
            />
          )}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
        <Select
          value={shareType}
          onChange={setShareType}
          style={{ width: 160 }}
          options={[
            { label: uiText('取件码管理'), value: 'pickup' },
            { label: uiText('分享链接'), value: 'link' },
            { label: uiText('全部分享'), value: 'all' },
          ]}
        />
        <Input.Search
          allowClear
          style={{ width: 260 }}
          placeholder={uiText('搜索取件码 / 用户名 / 文件名')}
          onSearch={(value) => setKeyword(value.trim())}
          onClear={() => setKeyword('')}
        />
        <Button icon={<IconRefresh />} loading={operating} onClick={releaseCodes}>
          {uiText('一键释放失效取件码')}
        </Button>
        <Button loading={loading} onClick={load}>
          {uiText('刷新')}
        </Button>
      </div>
      <Table
        rowKey="id"
        size="small"
        loading={loading}
        columns={columns}
        data={items}
        pagination={{ pageSize: 20, sizeCanChange: true }}
        scroll={{ x: 1500 }}
      />
    </div>
  );
}
