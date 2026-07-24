import React, {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react';
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
  Tabs,
  Tag,
  Typography,
} from '@arco-design/web-react';
import {
  IconDelete,
  IconEdit,
  IconLock,
  IconPlus,
  IconSearch,
  IconSettings,
  IconStop,
  IconUserGroup,
} from '@arco-design/web-react/icon';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
import { GlobalContext } from '@/context';
const TabPane = Tabs.TabPane;
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
  createdAt?: string;
}
function errorMessage(error: unknown, fallback: string) {
  return axios.isAxiosError(error) && error.response?.data?.msg
    ? error.response.data.msg
    : fallback;
}
function Users() {
  const { userInfo } = useContext(GlobalContext);
  const adminPermissions = userInfo?.adminPermissions || [];
  const canManageUsers = Boolean(
    userInfo?.isSuperAdmin || adminPermissions.includes('manage_users')
  );
  const canManageGroups = Boolean(
    userInfo?.isSuperAdmin || adminPermissions.includes('manage_user_groups')
  );
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
  const [editingGroup, setEditingGroup] = useState<UserGroupItem>();
  const [editGroupName, setEditGroupName] = useState('');
  const [editGroupDescription, setEditGroupDescription] = useState('');
  const [membersGroup, setMembersGroup] = useState<UserGroupItem>();
  const [selectedMemberIds, setSelectedMemberIds] = useState<number[]>([]);
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
  const updateGroup = async () => {
    if (!editingGroup) return;
    const name = editGroupName.trim().toLowerCase();
    if (!/^[a-z][a-z0-9_]{1,31}$/.test(name)) {
      Message.warning(
        uiText('名称须为 2 至 32 位小写字母、数字或下划线，并以字母开头')
      );
      return;
    }
    setSaving(true);
    try {
      await axios.put(`/api/admin/user-groups/${editingGroup.id}`, {
        name,
        description: editGroupDescription.trim(),
      });
      Message.success(uiText('用户组已更新'));
      setEditingGroup(undefined);
      await load();
    } catch (error) {
      Message.error(errorMessage(error, uiText('更新用户组失败')));
    } finally {
      setSaving(false);
    }
  };
  const deleteGroup = (group: UserGroupItem) => {
    const memberCount = items.filter((item) =>
      item.groupIds.includes(group.id)
    ).length;
    Modal.confirm({
      title: `${uiText('删除用户组')} · ${group.name}`,
      content: memberCount
        ? `${uiText('删除后，该组内的')} ${memberCount} ${uiText(
            '名用户将失去此用户组提供的权限与配额。'
          )}`
        : uiText('删除后无法恢复。'),
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await axios.delete(`/api/admin/user-groups/${group.id}`);
          Message.success(uiText('用户组已删除'));
          await load();
        } catch (error) {
          Message.error(errorMessage(error, uiText('删除用户组失败')));
          throw error;
        }
      },
    });
  };
  const saveGroupMembers = async () => {
    if (!membersGroup) return;
    setSaving(true);
    try {
      await axios.put(`/api/admin/user-groups/${membersGroup.id}/members`, {
        userIds: selectedMemberIds.filter(
          (id) => items.find((item) => item.id === id)?.username !== superAdmin
        ),
      });
      Message.success(uiText('用户组成员已更新'));
      setMembersGroup(undefined);
      await load();
    } catch (error) {
      Message.error(errorMessage(error, uiText('配置用户组成员失败')));
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
  const renderUserActions = (record: UserItem) => {
    const protectedUser = record.username === superAdmin;
    return (
      <Space size={4} wrap>
        {canManageGroups && (
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
        )}
        {canManageUsers && (
          <>
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
          </>
        )}
      </Space>
    );
  };
  const columns = [
    {
      title: uiText('用户'),
      dataIndex: 'username',
      width: 230,
      render: (value, record: UserItem) => (
        <div className={styles['user-name-cell']}>
          <div className={styles['user-name-heading']}>
            <b title={value}>{value}</b>
            {value === superAdmin && (
              <Tag color="arcoblue">{uiText('系统超级管理员')}</Tag>
            )}
            {record.disabled && <Tag color="red">{uiText('已禁用')}</Tag>}
          </div>
          <div className={styles['admin-mobile-meta']}>
            <div>
              <span className={styles['admin-mobile-label']}>
                {uiText('所属用户组')}：
              </span>
              {(record.groups || []).length
                ? record.groups.join('、')
                : '-'}
            </div>
            <div>
              <span className={styles['admin-mobile-label']}>
                {uiText('创建时间')}：
              </span>
              {new Date(record.createdAt).toLocaleString('zh-CN', {
                hour12: false,
              })}
            </div>
            <div className={styles['admin-mobile-actions']}>
              {renderUserActions(record)}
            </div>
          </div>
        </div>
      ),
    },
    {
      title: uiText('所属用户组'),
      dataIndex: 'groups',
      width: 240,
      className: styles['mobile-hidden'],
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
      className: styles['mobile-hidden'],
      render: (value) =>
        new Date(value).toLocaleString('zh-CN', {
          hour12: false,
        }),
    },
    {
      title: uiText('操作'),
      fixed: 'right' as const,
      width: 310,
      className: styles['mobile-hidden'],
      render: (_, record: UserItem) => renderUserActions(record),
    },
  ];
  const groupColumns = [
    {
      title: uiText('用户组'),
      dataIndex: 'name',
      width: 220,
      render: (value, record: UserGroupItem) => (
        <div className={styles['user-name-cell']}>
          <b>{value}</b>
          {record.isSystem && (
            <Tag color="arcoblue">{uiText('系统用户组')}</Tag>
          )}
        </div>
      ),
    },
    {
      title: uiText('说明'),
      dataIndex: 'description',
      ellipsis: true,
      render: (value) => value || '-',
    },
    {
      title: uiText('成员'),
      width: 300,
      render: (_, group: UserGroupItem) => {
        const members = items.filter((item) =>
          item.groupIds.includes(group.id)
        );
        return members.length ? (
          <div className={styles['group-member-tags']}>
            {members.slice(0, 4).map((item) => (
              <Tag key={item.id}>{item.username}</Tag>
            ))}
            {members.length > 4 && <Tag>+{members.length - 4}</Tag>}
          </div>
        ) : (
          <Typography.Text type="secondary">
            {uiText('暂无成员')}
          </Typography.Text>
        );
      },
    },
    {
      title: uiText('操作'),
      width: 280,
      render: (_, group: UserGroupItem) => (
        <Space size={4} wrap>
          <Button
            size="mini"
            type="text"
            icon={<IconUserGroup />}
            onClick={() => {
              setSelectedMemberIds(
                items
                  .filter((item) => item.groupIds.includes(group.id))
                  .map((item) => item.id)
              );
              setMembersGroup(group);
            }}
          >
            {uiText('配置人员')}
          </Button>
          <Button
            size="mini"
            type="text"
            icon={<IconEdit />}
            disabled={group.isSystem}
            onClick={() => {
              setEditGroupName(group.name);
              setEditGroupDescription(group.description || '');
              setEditingGroup(group);
            }}
          >
            {uiText('编辑')}
          </Button>
          <Button
            size="mini"
            type="text"
            status="danger"
            icon={<IconDelete />}
            disabled={group.isSystem}
            onClick={() => deleteGroup(group)}
          >
            {uiText('删除')}
          </Button>
        </Space>
      ),
    },
  ];
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('用户管理')}
        description={uiText('分别管理站点用户和用户组。')}
      />
      <Card className={styles['table-card']}>
        <Tabs defaultActiveTab={canManageUsers ? 'users' : 'groups'}>
          {canManageUsers && (
            <TabPane key="users" title={uiText('用户管理')}>
            <div className={styles.toolbar}>
              <Input
                allowClear
                prefix={<IconSearch />}
                placeholder={uiText('搜索用户名')}
                value={keyword}
                onChange={setKeyword}
                style={{ width: 280 }}
              />
              <Tag>
                {data.length}
                {uiText('个账户')}
              </Tag>
            </div>
            <Table
              className={styles['responsive-user-table']}
              rowKey="id"
              loading={loading}
              columns={columns}
              data={data}
              pagination={{ pageSize: 10, showTotal: true }}
              scroll={{ x: 970 }}
            />
            </TabPane>
          )}
          {canManageGroups && (
            <TabPane key="groups" title={uiText('用户组管理')}>
            <div className={styles['group-section-toolbar']}>
              <div>
                <Typography.Title heading={6}>
                  {uiText('用户组管理')}
                </Typography.Title>
                <Typography.Text type="secondary">
                  {uiText('新增、编辑、删除用户组，并配置组内人员。')}
                </Typography.Text>
              </div>
              <Button
                type="primary"
                icon={<IconPlus />}
                onClick={() => setGroupModalVisible(true)}
              >
                {uiText('新增用户组')}
              </Button>
            </div>
            <Table
              rowKey="id"
              loading={loading}
              columns={groupColumns}
              data={groups}
              pagination={false}
              scroll={{ x: 920 }}
            />
            </TabPane>
          )}
        </Tabs>
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
        title={`${uiText('编辑用户组')}${
          editingGroup ? ` · ${editingGroup.name}` : ''
        }`}
        visible={Boolean(editingGroup)}
        confirmLoading={saving}
        onOk={updateGroup}
        onCancel={() => setEditingGroup(undefined)}
        unmountOnExit
      >
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <div>
            <Typography.Text>{uiText('用户组名称')}</Typography.Text>
            <Input
              value={editGroupName}
              maxLength={32}
              onChange={setEditGroupName}
            />
          </div>
          <div>
            <Typography.Text>{uiText('说明')}</Typography.Text>
            <Input.TextArea
              value={editGroupDescription}
              maxLength={500}
              autoSize={{ minRows: 3, maxRows: 6 }}
              onChange={setEditGroupDescription}
            />
          </div>
        </Space>
      </Modal>

      <Modal
        title={`${uiText('配置人员')}${
          membersGroup ? ` · ${membersGroup.name}` : ''
        }`}
        visible={Boolean(membersGroup)}
        confirmLoading={saving}
        onOk={saveGroupMembers}
        onCancel={() => setMembersGroup(undefined)}
        unmountOnExit
      >
        <Typography.Paragraph type="secondary">
          {uiText('选择需要加入此用户组的人员，系统超级管理员不会被变更。')}
        </Typography.Paragraph>
        <Select
          mode="multiple"
          allowClear
          value={selectedMemberIds}
          placeholder={uiText('选择人员')}
          onChange={setSelectedMemberIds}
          style={{ width: '100%' }}
        >
          {items.map((item) => (
            <Select.Option
              key={item.id}
              value={item.id}
              disabled={item.username === superAdmin}
            >
              {item.username}
              {item.username === superAdmin ? uiText('（系统超级管理员）') : ''}
            </Select.Option>
          ))}
        </Select>
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
            {uiText('保存后该用户的全部现有登录会话会立即失效，动态令牌绑定也会清除，用户可在下次登录后重新绑定。')}
          </Typography.Text>
        </Space>
      </Modal>
    </div>
  );
}
export default Users;
