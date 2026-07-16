import React, { useCallback, useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Card,
  Input,
  Message,
  Modal,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from '@arco-design/web-react';
import {
  IconDelete,
  IconLock,
  IconPlus,
  IconSearch,
  IconSettings,
  IconStop,
} from '@arco-design/web-react/icon';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
interface UserItem {
  id: number;
  username: string;
  groupIds: number[];
  groups: string[];
  disabled: boolean;
  createdAt: string;
}
interface UserGroupItem {
  id: number;
  name: string;
  description?: string;
  isSystem: boolean;
}
function errorMessage(error: unknown, fallback: string) {
  return axios.isAxiosError(error) && error.response?.data?.msg
    ? error.response.data.msg
    : fallback;
}
function Users() {
  const [items, setItems] = useState<UserItem[]>([]);
  const [groups, setGroups] = useState<UserGroupItem[]>([]);
  const [superAdmin, setSuperAdmin] = useState('');
  const [keyword, setKeyword] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [groupTarget, setGroupTarget] = useState<UserItem>();
  const [selectedGroupIds, setSelectedGroupIds] = useState<number[]>([]);
  const [passwordTarget, setPasswordTarget] = useState<UserItem>();
  const [password, setPassword] = useState('');
  const [passwordAgain, setPasswordAgain] = useState('');
  const [groupModalVisible, setGroupModalVisible] = useState(false);
  const [newGroupName, setNewGroupName] = useState('');
  const [newGroupDescription, setNewGroupDescription] = useState('');
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await axios.get('/api/admin/users');
      setItems(res.data.items || []);
      setGroups(res.data.groups || []);
      setSuperAdmin(res.data.superAdminUsername || '');
    } catch (error) {
      Message.error(errorMessage(error, uiText('用户列表加载失败')));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);
  const data = useMemo(
    () =>
      items.filter((item) =>
        item.username.toLowerCase().includes(keyword.trim().toLowerCase())
      ),
    [items, keyword]
  );
  const createGroup = async () => {
    const name = newGroupName.trim().toLowerCase();
    if (!/^[a-z][a-z0-9_]{1,31}$/.test(name)) {
      Message.warning(
        uiText('名称须为 2 至 32 位小写字母、数字或下划线，并以字母开头')
      );
      return;
    }
    setSaving(true);
    try {
      await axios.post('/api/admin/user-groups', {
        name,
        description: newGroupDescription.trim(),
      });
      Message.success(uiText('用户组已新增'));
      setGroupModalVisible(false);
      setNewGroupName('');
      setNewGroupDescription('');
      await load();
    } catch (error) {
      Message.error(errorMessage(error, uiText('新增用户组失败')));
    } finally {
      setSaving(false);
    }
  };
  const saveUserGroups = async () => {
    if (!groupTarget) return;
    setSaving(true);
    try {
      await axios.put(`/api/admin/users/${groupTarget.id}/groups`, {
        groupIds: selectedGroupIds,
      });
      Message.success(uiText('用户组归属已更新'));
      setGroupTarget(undefined);
      await load();
    } catch (error) {
      Message.error(errorMessage(error, uiText('更新用户组失败')));
    } finally {
      setSaving(false);
    }
  };
  const resetPassword = async () => {
    if (!passwordTarget) return;
    if (password.length < 8) {
      Message.warning(uiText('密码至少需要 8 个字符'));
      return;
    }
    if (password !== passwordAgain) {
      Message.warning(uiText('两次输入的密码不一致'));
      return;
    }
    setSaving(true);
    try {
      await axios.put(`/api/admin/users/${passwordTarget.id}/password`, {
        password,
      });
      Message.success(uiText('密码已重设，该用户的现有登录已失效'));
      setPasswordTarget(undefined);
      setPassword('');
      setPasswordAgain('');
    } catch (error) {
      Message.error(errorMessage(error, uiText('重设密码失败')));
    } finally {
      setSaving(false);
    }
  };
  const changeDisabled = (item: UserItem) => {
    const disabled = !item.disabled;
    Modal.confirm({
      title: `${disabled ? uiText('禁用') : uiText('启用')} ${item.username}`,
      content: disabled
        ? uiText('禁用后该用户将立即退出登录，但文件和分享数据会保留。')
        : uiText('启用后该用户可以重新登录。'),
      okButtonProps: disabled
        ? {
            status: 'danger',
          }
        : undefined,
      onOk: async () => {
        try {
          await axios.put(`/api/admin/users/${item.id}/disabled`, {
            disabled,
          });
          Message.success(
            disabled ? uiText('用户已禁用') : uiText('用户已启用')
          );
          await load();
        } catch (error) {
          Message.error(errorMessage(error, uiText('更新用户状态失败')));
          throw error;
        }
      },
    });
  };
  const deleteUser = (item: UserItem) => {
    Modal.confirm({
      title: `${uiText('删除用户')} ${item.username}`,
      content: uiText(
        '该用户的文件、分享、直链和登录会话将一并删除，此操作不可恢复。'
      ),
      okButtonProps: {
        status: 'danger',
      },
      onOk: async () => {
        try {
          await axios.delete(`/api/admin/users/${item.id}`);
          Message.success(uiText('用户已删除'));
          await load();
        } catch (error) {
          Message.error(errorMessage(error, uiText('删除用户失败')));
          throw error;
        }
      },
    });
  };
  const columns = [
    {
      title: uiText('用户'),
      dataIndex: 'username',
      width: 230,
      render: (value, record: UserItem) => (
        <div className={styles['user-name-cell']}>
          <b title={value}>{value}</b>
          {value === superAdmin && (
            <Tag color="arcoblue">{uiText('系统超级管理员')}</Tag>
          )}
          {record.disabled && <Tag color="red">{uiText('已禁用')}</Tag>}
        </div>
      ),
    },
    {
      title: uiText('所属用户组'),
      dataIndex: 'groups',
      width: 240,
      render: (values: string[] = []) =>
        values.length ? (
          values.map((name) => <Tag key={name}>{name}</Tag>)
        ) : (
          <span>-</span>
        ),
    },
    {
      title: uiText('创建时间'),
      dataIndex: 'createdAt',
      width: 190,
      render: (value) =>
        new Date(value).toLocaleString('zh-CN', {
          hour12: false,
        }),
    },
    {
      title: uiText('操作'),
      fixed: 'right' as const,
      width: 310,
      render: (_, record: UserItem) => {
        const protectedUser = record.username === superAdmin;
        return (
          <Space size={4} wrap>
            <Button
              size="mini"
              type="text"
              icon={<IconSettings />}
              disabled={protectedUser}
              onClick={() => {
                setSelectedGroupIds(record.groupIds || []);
                setGroupTarget(record);
              }}
            >
              {uiText('用户组')}
            </Button>
            <Button
              size="mini"
              type="text"
              icon={<IconLock />}
              disabled={protectedUser}
              onClick={() => setPasswordTarget(record)}
            >
              {uiText('重设密码')}
            </Button>
            <Button
              size="mini"
              type="text"
              status={record.disabled ? 'success' : 'warning'}
              icon={<IconStop />}
              disabled={protectedUser}
              onClick={() => changeDisabled(record)}
            >
              {record.disabled ? uiText('启用') : uiText('禁用')}
            </Button>
            <Button
              size="mini"
              type="text"
              status="danger"
              icon={<IconDelete />}
              disabled={protectedUser}
              onClick={() => deleteUser(record)}
            >
              {uiText('删除')}
            </Button>
          </Space>
        );
      },
    },
  ];
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('用户管理')}
        description={uiText('管理站点账户、用户组归属、登录状态和密码。')}
        extra={
          <Button
            type="primary"
            icon={<IconPlus />}
            onClick={() => setGroupModalVisible(true)}
          >
            {uiText('新增用户组')}
          </Button>
        }
      />
      <Card className={styles['table-card']}>
        <div className={styles.toolbar}>
          <Input
            allowClear
            prefix={<IconSearch />}
            placeholder={uiText('搜索用户名')}
            value={keyword}
            onChange={setKeyword}
            style={{
              width: 280,
            }}
          />
          <Tag>
            {data.length}
            {uiText('个账户')}
          </Tag>
        </div>
        <Table
          rowKey="id"
          loading={loading}
          columns={columns}
          data={data}
          pagination={{
            pageSize: 10,
            showTotal: true,
          }}
          scroll={{
            x: 970,
          }}
        />
      </Card>

      <Modal
        title={uiText('新增用户组')}
        visible={groupModalVisible}
        confirmLoading={saving}
        onOk={createGroup}
        onCancel={() => setGroupModalVisible(false)}
        unmountOnExit
      >
        <Space
          direction="vertical"
          size={16}
          style={{
            width: '100%',
          }}
        >
          <div>
            <Typography.Text>{uiText('用户组名称')}</Typography.Text>
            <Input
              value={newGroupName}
              maxLength={32}
              placeholder={uiText('例如 project_member')}
              onChange={setNewGroupName}
            />
            <Typography.Text type="secondary">
              {uiText('使用小写字母、数字和下划线，并以字母开头')}
            </Typography.Text>
          </div>
          <div>
            <Typography.Text>{uiText('说明')}</Typography.Text>
            <Input.TextArea
              value={newGroupDescription}
              maxLength={500}
              autoSize={{
                minRows: 3,
                maxRows: 6,
              }}
              placeholder={uiText('说明该用户组的用途')}
              onChange={setNewGroupDescription}
            />
          </div>
        </Space>
      </Modal>

      <Modal
        title={`${uiText('修改用户组')}${
          groupTarget ? ` · ${groupTarget.username}` : ''
        }`}
        visible={Boolean(groupTarget)}
        confirmLoading={saving}
        onOk={saveUserGroups}
        onCancel={() => setGroupTarget(undefined)}
        unmountOnExit
      >
        <Typography.Paragraph type="secondary">
          {uiText(
            '用户将继承所选用户组对应的权限与配额。允许不选择任何用户组。'
          )}
        </Typography.Paragraph>
        <Select
          mode="multiple"
          allowClear
          placeholder={uiText('选择用户组')}
          value={selectedGroupIds}
          onChange={setSelectedGroupIds}
          style={{
            width: '100%',
          }}
        >
          {groups.map((group) => (
            <Select.Option key={group.id} value={group.id}>
              {group.name}
              {group.isSystem ? uiText('（系统）') : ''}
            </Select.Option>
          ))}
        </Select>
      </Modal>

      <Modal
        title={`${uiText('重设密码')}${
          passwordTarget ? ` · ${passwordTarget.username}` : ''
        }`}
        visible={Boolean(passwordTarget)}
        confirmLoading={saving}
        onOk={resetPassword}
        onCancel={() => {
          setPasswordTarget(undefined);
          setPassword('');
          setPasswordAgain('');
        }}
        unmountOnExit
      >
        <Space
          direction="vertical"
          size={14}
          style={{
            width: '100%',
          }}
        >
          <Input.Password
            value={password}
            maxLength={1024}
            placeholder={uiText('输入新密码（至少 8 个字符）')}
            onChange={setPassword}
          />
          <Input.Password
            value={passwordAgain}
            maxLength={1024}
            placeholder={uiText('再次输入新密码')}
            onChange={setPasswordAgain}
          />
          <Typography.Text type="secondary">
            {uiText('保存后该用户的全部现有登录会话会立即失效。')}
          </Typography.Text>
        </Space>
      </Modal>
    </div>
  );
}
export default Users;
