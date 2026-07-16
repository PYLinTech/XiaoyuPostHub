import React, { useCallback, useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Card,
  Checkbox,
  Grid,
  Input,
  InputNumber,
  Message,
  Modal,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Typography,
} from '@arco-design/web-react';
import { IconDelete, IconEdit, IconPlus } from '@arco-design/web-react/icon';
import { AdminPageHeader, formatBytes, formatLimit } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
const TabPane = Tabs.TabPane;
const MiB = 1024 * 1024;
interface PermissionDefinition {
  code: string;
  description: string;
}
interface QuotaItem {
  id: number;
  name: string;
  description?: string;
  storageBytesLimit?: number;
  singleFileBytesLimit?: number;
  dailyUploadBytesLimit?: number;
  dailyUploadCountLimit?: number;
  activeShareCountLimit?: number;
  activeDirectLinkLimit?: number;
  isSystem: boolean;
}
interface GroupItem {
  id: number;
  name: string;
  description?: string;
  isSystem: boolean;
  quotaProfileId?: number;
  priority: number;
  permissions: string[];
}
interface QuotaDraft {
  name: string;
  description: string;
  storageMiB?: number;
  singleFileMiB?: number;
  dailyUploadMiB?: number;
  dailyUploadCount?: number;
  activeShares?: number;
  activeDirectLinks?: number;
}
interface InvitationData {
  registrationRequiresInvitation: boolean;
  users: {
    id: number;
    name: string;
  }[];
  groups: {
    id: number;
    name: string;
  }[];
  items: InvitationItem[];
}
interface InvitationItem {
  id: number;
  codePrefix: string;
  targetType: 'user' | 'group';
  targetName: string;
  status: keyof typeof invitationStatus;
  usedBy?: string;
  createdAt: string;
}
const emptyQuota: QuotaDraft = {
  name: '',
  description: '',
};
const invitationStatus = {
  available: {
    label: uiText('可使用'),
    color: 'green',
  },
  used: {
    label: uiText('已使用'),
    color: 'arcoblue',
  },
  revoked: {
    label: uiText('已作废'),
    color: 'gray',
  },
};
function errorMessage(error: unknown, fallback: string) {
  return axios.isAxiosError(error) && error.response?.data?.msg
    ? error.response.data.msg
    : fallback;
}
function toDraft(item: QuotaItem): QuotaDraft {
  return {
    name: item.name,
    description: item.description || '',
    storageMiB:
      item.storageBytesLimit == null ? undefined : item.storageBytesLimit / MiB,
    singleFileMiB:
      item.singleFileBytesLimit == null
        ? undefined
        : item.singleFileBytesLimit / MiB,
    dailyUploadMiB:
      item.dailyUploadBytesLimit == null
        ? undefined
        : item.dailyUploadBytesLimit / MiB,
    dailyUploadCount: item.dailyUploadCountLimit,
    activeShares: item.activeShareCountLimit,
    activeDirectLinks: item.activeDirectLinkLimit,
  };
}
function quotaPayload(draft: QuotaDraft) {
  const bytes = (value?: number) =>
    value == null ? null : Math.round(value * MiB);
  return {
    name: draft.name.trim().toLowerCase(),
    description: draft.description.trim(),
    storageBytesLimit: bytes(draft.storageMiB),
    singleFileBytesLimit: bytes(draft.singleFileMiB),
    dailyUploadBytesLimit: bytes(draft.dailyUploadMiB),
    dailyUploadCountLimit: draft.dailyUploadCount ?? null,
    activeShareCountLimit: draft.activeShares ?? null,
    activeDirectLinkLimit: draft.activeDirectLinks ?? null,
  };
}
function Access() {
  const [permissions, setPermissions] = useState<PermissionDefinition[]>([]);
  const [quotas, setQuotas] = useState<QuotaItem[]>([]);
  const [groups, setGroups] = useState<GroupItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [quotaEditing, setQuotaEditing] = useState<QuotaItem | null>();
  const [quotaDraft, setQuotaDraft] = useState<QuotaDraft>(emptyQuota);
  const [permissionGroup, setPermissionGroup] = useState<GroupItem>();
  const [selectedPermissions, setSelectedPermissions] = useState<string[]>([]);
  const [groupQuotaDrafts, setGroupQuotaDrafts] = useState<
    Record<
      number,
      {
        quotaProfileId?: number;
        priority: number;
      }
    >
  >({});
  const [invitationLoading, setInvitationLoading] = useState(true);
  const [invitationData, setInvitationData] = useState<InvitationData>({
    registrationRequiresInvitation: false,
    users: [],
    groups: [],
    items: [],
  });
  const [targetType, setTargetType] = useState('user');
  const [targetId, setTargetId] = useState<number>();
  const [quantity, setQuantity] = useState(1);
  const [issuing, setIssuing] = useState(false);
  const loadAccess = useCallback(async () => {
    setLoading(true);
    try {
      const res = await axios.get('/api/admin/access');
      const nextGroups: GroupItem[] = res.data.groups || [];
      setPermissions(res.data.availablePermissions || []);
      setQuotas(res.data.quotas || []);
      setGroups(nextGroups);
      setGroupQuotaDrafts(
        Object.fromEntries(
          nextGroups.map((group) => [
            group.id,
            {
              quotaProfileId: group.quotaProfileId,
              priority: group.priority || 0,
            },
          ])
        )
      );
    } catch (error) {
      Message.error(errorMessage(error, uiText('权限与配额加载失败')));
    } finally {
      setLoading(false);
    }
  }, []);
  const loadInvitations = useCallback(async () => {
    setInvitationLoading(true);
    try {
      const res = await axios.get('/api/admin/invitations');
      setInvitationData(res.data.data || invitationData);
    } catch (error) {
      Message.error(errorMessage(error, uiText('邀请码配置加载失败')));
    } finally {
      setInvitationLoading(false);
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    loadAccess();
    loadInvitations();
  }, [loadAccess, loadInvitations]);
  const saveQuota = async () => {
    if (!quotaEditing && !/^[a-z][a-z0-9_]{1,31}$/.test(quotaDraft.name)) {
      Message.warning(uiText('方案名称须为 2 至 32 位小写字母、数字或下划线'));
      return;
    }
    setSaving(true);
    try {
      if (quotaEditing) {
        await axios.put(
          `/api/admin/access/quotas/${quotaEditing.id}`,
          quotaPayload(quotaDraft)
        );
      } else {
        await axios.post('/api/admin/access/quotas', quotaPayload(quotaDraft));
      }
      Message.success(
        quotaEditing ? uiText('配额方案已更新') : uiText('配额方案已新增')
      );
      setQuotaEditing(undefined);
      setQuotaDraft(emptyQuota);
      await loadAccess();
    } catch (error) {
      Message.error(errorMessage(error, uiText('保存配额方案失败')));
    } finally {
      setSaving(false);
    }
  };
  const deleteQuota = (item: QuotaItem) => {
    Modal.confirm({
      title: `${uiText('删除配额方案')} ${item.name}`,
      content: uiText('仅未被任何用户组使用的非系统方案可以删除。'),
      okButtonProps: {
        status: 'danger',
      },
      onOk: async () => {
        try {
          await axios.delete(`/api/admin/access/quotas/${item.id}`);
          Message.success(uiText('配额方案已删除'));
          await loadAccess();
        } catch (error) {
          Message.error(errorMessage(error, uiText('删除配额方案失败')));
          throw error;
        }
      },
    });
  };
  const saveGroupQuota = async (group: GroupItem) => {
    const draft = groupQuotaDrafts[group.id];
    if (!draft?.quotaProfileId) {
      Message.warning(uiText('请选择配额方案'));
      return;
    }
    try {
      await axios.put(`/api/admin/access/groups/${group.id}/quota`, draft);
      Message.success(`${uiText('配额配置已保存')}：${group.name}`);
      await loadAccess();
    } catch (error) {
      Message.error(errorMessage(error, uiText('保存用户组配额失败')));
    }
  };
  const savePermissions = async () => {
    if (!permissionGroup) return;
    setSaving(true);
    try {
      await axios.put(
        `/api/admin/access/groups/${permissionGroup.id}/permissions`,
        {
          permissions: selectedPermissions,
        }
      );
      Message.success(uiText('用户组权限已保存'));
      setPermissionGroup(undefined);
      await loadAccess();
    } catch (error) {
      Message.error(errorMessage(error, uiText('保存用户组权限失败')));
    } finally {
      setSaving(false);
    }
  };
  const targets =
    targetType === 'user' ? invitationData.users : invitationData.groups;
  const targetOptions = useMemo(
    () =>
      targets.map((item) => ({
        label: item.name,
        value: item.id,
      })),
    [targets]
  );
  const updateRequirement = async (checked: boolean) => {
    try {
      await axios.put('/api/admin/invitations/settings', {
        registrationRequiresInvitation: checked,
      });
      setInvitationData((current) => ({
        ...current,
        registrationRequiresInvitation: checked,
      }));
      Message.success(
        checked ? uiText('已开启邀请码注册') : uiText('已关闭邀请码注册要求')
      );
    } catch (error) {
      Message.error(errorMessage(error, uiText('注册策略更新失败')));
    }
  };
  const issueCodes = async () => {
    if (!targetId) {
      Message.warning(uiText('请选择发放对象'));
      return;
    }
    setIssuing(true);
    try {
      const res = await axios.post('/api/admin/invitations', {
        targetType,
        targetId,
        quantity,
      });
      Message.success(
        `${uiText('邀请码已生成并发送')}：${res.data.quantity || quantity}`
      );
      await loadInvitations();
    } catch (error) {
      Message.error(errorMessage(error, uiText('邀请码生成失败')));
    } finally {
      setIssuing(false);
    }
  };
  const quotaColumns = [
    {
      title: uiText('配额方案'),
      dataIndex: 'name',
      width: 210,
      render: (value, record: QuotaItem) => (
        <Space>
          <b>{value}</b>
          {record.isSystem && <Tag>{uiText('系统')}</Tag>}
        </Space>
      ),
    },
    {
      title: uiText('存储空间'),
      dataIndex: 'storageBytesLimit',
      render: (value) => (value == null ? uiText('不限') : formatBytes(value)),
    },
    {
      title: uiText('单文件'),
      dataIndex: 'singleFileBytesLimit',
      render: (value) => (value == null ? uiText('不限') : formatBytes(value)),
    },
    {
      title: uiText('每日上传'),
      dataIndex: 'dailyUploadBytesLimit',
      render: (value) => (value == null ? uiText('不限') : formatBytes(value)),
    },
    {
      title: uiText('每日次数'),
      dataIndex: 'dailyUploadCountLimit',
      render: formatLimit,
    },
    {
      title: uiText('分享 / 直链'),
      render: (_, record: QuotaItem) =>
        `${formatLimit(record.activeShareCountLimit)} / ${formatLimit(
          record.activeDirectLinkLimit
        )}`,
    },
    {
      title: uiText('操作'),
      width: 150,
      fixed: 'right' as const,
      render: (_, record: QuotaItem) => (
        <Space>
          <Button
            size="mini"
            type="text"
            icon={<IconEdit />}
            onClick={() => {
              setQuotaEditing(record);
              setQuotaDraft(toDraft(record));
            }}
          >
            {uiText('编辑')}
          </Button>
          {!record.isSystem && (
            <Button
              size="mini"
              type="text"
              status="danger"
              icon={<IconDelete />}
              onClick={() => deleteQuota(record)}
            >
              {uiText('删除')}
            </Button>
          )}
        </Space>
      ),
    },
  ];
  const groupQuotaColumns = [
    {
      title: uiText('用户组'),
      dataIndex: 'name',
      width: 220,
      render: (value, record: GroupItem) => (
        <Space>
          <b>{value}</b>
          {record.isSystem && <Tag>{uiText('系统')}</Tag>}
        </Space>
      ),
    },
    {
      title: uiText('配额方案'),
      render: (_, record: GroupItem) => (
        <Select
          value={groupQuotaDrafts[record.id]?.quotaProfileId}
          placeholder={uiText('选择方案')}
          style={{
            width: 210,
          }}
          onChange={(value) =>
            setGroupQuotaDrafts((current) => ({
              ...current,
              [record.id]: {
                ...current[record.id],
                quotaProfileId: value,
              },
            }))
          }
          options={quotas.map((quota) => ({
            label: quota.name,
            value: quota.id,
          }))}
        />
      ),
    },
    {
      title: uiText('优先级'),
      render: (_, record: GroupItem) => (
        <InputNumber
          value={groupQuotaDrafts[record.id]?.priority ?? 0}
          min={-10000}
          max={10000}
          style={{
            width: 130,
          }}
          onChange={(value) =>
            setGroupQuotaDrafts((current) => ({
              ...current,
              [record.id]: {
                ...current[record.id],
                priority: value || 0,
              },
            }))
          }
        />
      ),
    },
    {
      title: uiText('操作'),
      width: 100,
      render: (_, record: GroupItem) => (
        <Button
          type="primary"
          size="mini"
          onClick={() => saveGroupQuota(record)}
        >
          {uiText('保存')}
        </Button>
      ),
    },
  ];
  const permissionColumns = [
    {
      title: uiText('用户组'),
      dataIndex: 'name',
      width: 220,
      render: (value, record: GroupItem) => (
        <Space>
          <b>{value}</b>
          {record.isSystem && <Tag>{uiText('系统')}</Tag>}
        </Space>
      ),
    },
    {
      title: uiText('已授予权限'),
      dataIndex: 'permissions',
      render: (codes: string[]) =>
        codes.length ? (
          <Space wrap size={4}>
            {codes.map((code) => (
              <Tag key={code}>
                {permissions.find((item) => item.code === code)?.description ||
                  code}
              </Tag>
            ))}
          </Space>
        ) : (
          <Typography.Text type="secondary">
            {uiText('未授予权限')}
          </Typography.Text>
        ),
    },
    {
      title: uiText('操作'),
      width: 120,
      render: (_, record: GroupItem) => (
        <Button
          type="text"
          icon={<IconEdit />}
          onClick={() => {
            setPermissionGroup(record);
            setSelectedPermissions(record.permissions || []);
          }}
        >
          {uiText('配置')}
        </Button>
      ),
    },
  ];
  const invitationColumns = [
    {
      title: uiText('邀请码'),
      dataIndex: 'codePrefix',
      render: (value) => <code>{value}••••••</code>,
    },
    {
      title: uiText('发放对象'),
      render: (_, record) => (
        <Space>
          <Tag>
            {record.targetType === 'user' ? uiText('用户') : uiText('用户组')}
          </Tag>
          {record.targetName}
        </Space>
      ),
    },
    {
      title: uiText('状态'),
      dataIndex: 'status',
      render: (value) => (
        <Tag color={invitationStatus[value]?.color}>
          {invitationStatus[value]?.label || value}
        </Tag>
      ),
    },
    {
      title: uiText('使用人'),
      dataIndex: 'usedBy',
      render: (value) => value || '-',
    },
    {
      title: uiText('生成时间'),
      dataIndex: 'createdAt',
      render: (value) =>
        new Date(value).toLocaleString('zh-CN', {
          hour12: false,
        }),
    },
    {
      title: uiText('操作'),
      render: (_, record) =>
        record.status === 'available' ? (
          <Button
            type="text"
            status="danger"
            size="small"
            onClick={async () => {
              try {
                await axios.delete(`/api/admin/invitations/${record.id}`);
                Message.success(uiText('邀请码已作废'));
                await loadInvitations();
              } catch (error) {
                Message.error(errorMessage(error, uiText('作废失败')));
              }
            }}
          >
            {uiText('作废')}
          </Button>
        ) : (
          '-'
        ),
    },
  ];
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('权限与配额')}
        description={uiText(
          '权限和配额均直接配置到用户组，用户只继承所属用户组的设置。'
        )}
      />
      <Card className={styles['table-card']}>
        <Tabs defaultActiveTab="quotas">
          <TabPane key="quotas" title={uiText('配额方案')}>
            <div className={styles['access-section-header']}>
              <div>
                <Typography.Title heading={6}>
                  {uiText('配额方案')}
                </Typography.Title>
                <Typography.Text type="secondary">
                  {uiText('空值表示不限，0 表示禁止使用对应资源。')}
                </Typography.Text>
              </div>
              <Button
                type="primary"
                icon={<IconPlus />}
                onClick={() => {
                  setQuotaEditing(null);
                  setQuotaDraft(emptyQuota);
                }}
              >
                {uiText('新增方案')}
              </Button>
            </div>
            <Table
              rowKey="id"
              loading={loading}
              columns={quotaColumns}
              data={quotas}
              pagination={false}
              scroll={{
                x: 1020,
              }}
            />
            <div className={styles['access-section-header']}>
              <div>
                <Typography.Title heading={6}>
                  {uiText('用户组配额')}
                </Typography.Title>
                <Typography.Text type="secondary">
                  {uiText('用户属于多个用户组时，采用优先级最高的已绑定方案。')}
                </Typography.Text>
              </div>
            </div>
            <Table
              rowKey="id"
              loading={loading}
              columns={groupQuotaColumns}
              data={groups}
              pagination={false}
              scroll={{
                x: 760,
              }}
            />
          </TabPane>
          <TabPane key="permissions" title={uiText('权限配置')}>
            <Table
              rowKey="id"
              loading={loading}
              columns={permissionColumns}
              data={groups}
              pagination={false}
              scroll={{
                x: 820,
              }}
            />
          </TabPane>
          <TabPane key="invitations" title={uiText('邀请码')}>
            <div className={styles['invitation-policy']}>
              <div>
                <Typography.Title heading={6}>
                  {uiText('注册策略')}
                </Typography.Title>
                <Typography.Text type="secondary">
                  {uiText('开启后，公开注册必须提交一个有效邀请码。')}
                </Typography.Text>
              </div>
              <Checkbox
                checked={invitationData.registrationRequiresInvitation}
                onChange={updateRequirement}
              >
                {uiText('注册需要邀请码')}
              </Checkbox>
            </div>
            <div className={styles['invitation-issue']}>
              <Typography.Title heading={6}>
                {uiText('发放邀请码')}
              </Typography.Title>
              <Typography.Paragraph type="secondary">
                {uiText('完整邀请码通过右上角消息中心投递给接收方。')}
              </Typography.Paragraph>
              <Space wrap>
                <Select
                  value={targetType}
                  onChange={(value) => {
                    setTargetType(value);
                    setTargetId(undefined);
                  }}
                  style={{
                    width: 130,
                  }}
                  options={[
                    {
                      label: uiText('发放给用户'),
                      value: 'user',
                    },
                    {
                      label: uiText('发放给用户组'),
                      value: 'group',
                    },
                  ]}
                />
                <Select
                  value={targetId}
                  onChange={setTargetId}
                  placeholder={uiText('选择发放对象')}
                  showSearch
                  allowClear
                  style={{
                    width: 220,
                  }}
                  options={targetOptions}
                />
                <InputNumber
                  min={1}
                  max={100}
                  value={quantity}
                  onChange={setQuantity}
                  style={{
                    width: 140,
                  }}
                  suffix={uiText('个')}
                />
                <Button type="primary" loading={issuing} onClick={issueCodes}>
                  {uiText('生成邀请码')}
                </Button>
              </Space>
            </div>
            <Table
              rowKey="id"
              loading={invitationLoading}
              columns={invitationColumns}
              data={invitationData.items}
              pagination={{
                pageSize: 10,
                showTotal: true,
              }}
              scroll={{
                x: 900,
              }}
            />
          </TabPane>
        </Tabs>
      </Card>

      <Modal
        title={
          quotaEditing
            ? `${uiText('编辑配额方案')} · ${quotaEditing.name}`
            : uiText('新增配额方案')
        }
        visible={quotaEditing !== undefined}
        confirmLoading={saving}
        onOk={saveQuota}
        onCancel={() => setQuotaEditing(undefined)}
        style={{
          width: 'min(720px, 94vw)',
        }}
        unmountOnExit
      >
        <Grid.Row gutter={16}>
          <Grid.Col span={12} xs={24}>
            <Typography.Text>{uiText('方案名称')}</Typography.Text>
            <Input
              value={quotaDraft.name}
              disabled={Boolean(quotaEditing)}
              placeholder={uiText('例如 standard_user')}
              onChange={(name) =>
                setQuotaDraft((draft) => ({
                  ...draft,
                  name,
                }))
              }
            />
          </Grid.Col>
          <Grid.Col span={12} xs={24}>
            <Typography.Text>{uiText('说明')}</Typography.Text>
            <Input
              value={quotaDraft.description}
              placeholder={uiText('方案用途')}
              onChange={(description) =>
                setQuotaDraft((draft) => ({
                  ...draft,
                  description,
                }))
              }
            />
          </Grid.Col>
        </Grid.Row>
        <div className={styles['quota-field-grid']}>
          {[
            ['storageMiB', uiText('存储空间'), 'MiB'],
            ['singleFileMiB', uiText('单文件大小'), 'MiB'],
            ['dailyUploadMiB', uiText('每日上传流量'), 'MiB'],
            ['dailyUploadCount', uiText('每日上传次数'), uiText('次')],
            ['activeShares', uiText('有效分享数量'), uiText('个')],
            ['activeDirectLinks', uiText('有效直链数量'), uiText('个')],
          ].map(([key, label, suffix]) => (
            <div key={key}>
              <Typography.Text>{label}</Typography.Text>
              <InputNumber
                min={0}
                precision={suffix === 'MiB' ? 2 : 0}
                value={quotaDraft[key]}
                placeholder={uiText('不限')}
                suffix={suffix}
                onChange={(value) =>
                  setQuotaDraft((draft) => ({
                    ...draft,
                    [key]: value,
                  }))
                }
              />
            </div>
          ))}
        </div>
      </Modal>

      <Modal
        title={`${uiText('配置权限')}${
          permissionGroup ? ` · ${permissionGroup.name}` : ''
        }`}
        visible={Boolean(permissionGroup)}
        confirmLoading={saving}
        onOk={savePermissions}
        onCancel={() => setPermissionGroup(undefined)}
        style={{
          width: 'min(720px, 94vw)',
        }}
        unmountOnExit
      >
        <Checkbox.Group
          value={selectedPermissions}
          onChange={setSelectedPermissions}
          className={styles['permission-checkbox-grid']}
        >
          {permissions.map((item) => (
            <Checkbox key={item.code} value={item.code}>
              <span>{item.description}</span>
              <code>{item.code}</code>
            </Checkbox>
          ))}
        </Checkbox.Group>
      </Modal>
    </div>
  );
}
export default Access;
