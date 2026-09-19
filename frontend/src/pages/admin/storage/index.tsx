import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Message,
  Modal,
  Radio,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  Typography,
} from '@arco-design/web-react';
import { IconPlus, IconRefresh } from '@arco-design/web-react/icon';
import {
  cancelAdminUpload,
  createAdminStorageTask,
  fetchAdminStorageBackends,
  fetchAdminStorageTaskItems,
  fetchAdminStorageTasks,
  fetchAdminUploads,
  saveAdminStorageBackend,
} from '@/api/endpoints';
import { apiErrorMessage } from '@/api/client';
import { formatBytes, formatTime } from '@/utils/format';
import uiText from '@/utils/uiText';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';

interface StorageBackend {
  id: number;
  name: string;
  kind: 'local' | 'pan123' | 's3' | string;
  settings: Record<string, unknown>;
  isEnabled: boolean;
  isDefault: boolean;
  ready: boolean;
  usedBytes: number;
}

/** 维护选项：孤立文件 / 加密 / 解密 / 分片 / 迁移。 */
type MaintKind = 'orphan' | 'encrypt' | 'decrypt' | 'chunk' | 'migrate';

interface TaskItem {
  id: number;
  type: 'migrate' | 'encrypt' | 'decrypt' | 'purge_orphan' | 'chunk' | string;
  /** scan=只扫描出清单（不改动数据）；apply=按清单执行。 */
  phase: 'scan' | 'apply' | string;
  sourceTaskId?: number | null;
  status: 'queued' | 'running' | 'done' | 'failed' | 'canceled' | string;
  totalItems: number;
  doneItems: number;
  failedItems: number;
  error?: string | null;
  createdAt: string;
}

/** 任务清单里的单个对象。 */
interface TaskObjectItem {
  blobId: string;
  status: string;
  error?: string;
  sizeBytes: number;
  name?: string;
}

const kindLabel = (kind: string) => {
  if (kind === 'local') return uiText('本机存储');
  if (kind === 'pan123') return uiText('123 云盘');
  if (kind === 's3') return uiText('对象存储');
  return kind;
};

/**
 * 存储后端管理：登记本机 / 第三方网盘后端并选择默认上传目标。
 * 敏感凭据（123 云盘 client_secret）只来自部署 .env，不在此填写。
 */
export default function StorageConfig() {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [items, setItems] = useState<StorageBackend[]>([]);
  const [pan123Ready, setPan123Ready] = useState(false);
  const [s3Ready, setS3Ready] = useState(false);
  const [tasks, setTasks] = useState<TaskItem[]>([]);
  const [taskType, setTaskType] = useState<MaintKind>('orphan');
  // 迁移：原位置（留空＝除新位置外全部）与新位置（必选）。
  const [taskSource, setTaskSource] = useState<number>();
  const [taskTarget, setTaskTarget] = useState<number>();
  // 加密/解密/分片：扫描范围（留空＝全部后端）。
  const [scopeBackend, setScopeBackend] = useState<number>();
  const [chunkSizeMB, setChunkSizeMB] = useState(64);
  const [creatingTask, setCreatingTask] = useState(false);
  // 结果清单弹窗（扫描结果 / 执行明细）。
  const [detailTask, setDetailTask] = useState<TaskItem | null>(null);
  const [detailItems, setDetailItems] = useState<TaskObjectItem[]>([]);
  const [detailLoading, setDetailLoading] = useState(false);
  const [tasksLoading, setTasksLoading] = useState(false);
  const [editing, setEditing] = useState<StorageBackend | null>(null);
  const [visible, setVisible] = useState(false);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await fetchAdminStorageBackends();
      setItems(res.data.items || []);
      setPan123Ready(Boolean(res.data.pan123CredentialsConfigured));
      setS3Ready(Boolean(res.data.s3CredentialsConfigured));
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('读取存储后端失败')));
    } finally {
      setLoading(false);
    }
  }, []);

  // 轮询失败降噪：连续失败只在首次提示，成功后复位。
  const tasksErrorShown = useRef(false);
  const loadTasks = useCallback(async () => {
    setTasksLoading(true);
    try {
      const res = await fetchAdminStorageTasks();
      setTasks(res.data.items || []);
      tasksErrorShown.current = false;
    } catch (error) {
      if (!tasksErrorShown.current) {
        tasksErrorShown.current = true;
        Message.error(apiErrorMessage(error, uiText('读取存储任务失败')));
      }
    } finally {
      setTasksLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    loadTasks();
  }, [load, loadTasks]);

  // 有排队/执行中的任务时轮询刷新进度。定时器只依赖"是否存在活动任务"：
  // 直接依赖 tasks 会在每次刷新后重建定时器（周期抖动、提示重复）。
  const tasksActive = tasks.some(
    (task) => task.status === 'queued' || task.status === 'running'
  );
  const loadTasksRef = useRef(loadTasks);
  useEffect(() => {
    loadTasksRef.current = loadTasks;
  }, [loadTasks]);
  useEffect(() => {
    if (!tasksActive) return undefined;
    const timer = window.setInterval(() => {
      void loadTasksRef.current();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [tasksActive]);

  // 在途上传任务：占用临时盘，管理员可直接取消（不必等待会话过期）。
  interface AdminUploadItem {
    id: string;
    ownerName: string;
    filename: string;
    totalSize: number;
    status: string;
    updatedAt: string;
  }
  const [uploads, setUploads] = useState<AdminUploadItem[]>([]);
  const [uploadsLoading, setUploadsLoading] = useState(false);
  const loadUploads = useCallback(async () => {
    setUploadsLoading(true);
    try {
      const res = await fetchAdminUploads();
      setUploads(res.data.items || []);
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('读取在途上传失败')));
    } finally {
      setUploadsLoading(false);
    }
  }, []);
  useEffect(() => {
    loadUploads();
  }, [loadUploads]);
  const cancelUpload = (item: AdminUploadItem) => {
    Modal.confirm({
      title: uiText('取消任务'),
      content: `${uiText('将取消该上传任务并清理已上传的分片')}：${item.filename}`,
      okButtonProps: { status: 'danger' },
      onOk: async () => {
        try {
          await cancelAdminUpload(item.id);
          Message.success(uiText('上传任务已取消'));
          await loadUploads();
        } catch (error) {
          Message.error(apiErrorMessage(error, uiText('操作失败')));
        }
      },
    });
  };

  // 扫描：只产出"命中清单"，不改动任何数据。
  const doScan = async () => {
    try {
      setCreatingTask(true);
      const res = await createAdminStorageTask({
        action: 'scan',
        type: taskType,
        backendId: taskType === 'orphan' ? undefined : scopeBackend,
        chunkSize: taskType === 'chunk' ? chunkSizeMB * 1024 * 1024 : undefined,
      });
      const task = res.data.task as TaskItem;
      if (task && task.totalItems === 0) {
        Message.info(uiText('扫描完成：没有需要处理的对象'));
      } else {
        Message.success(
          `${uiText('扫描完成，命中')} ${task.totalItems} ${uiText('个对象，确认后可在下方执行')}`
        );
      }
      await loadTasks();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('扫描失败')));
    } finally {
      setCreatingTask(false);
    }
  };

  // 执行：针对某个扫描任务的结果清单（清单在创建时已复制，不受后续数据变化影响）。
  const doApply = async (task: TaskItem) => {
    try {
      setCreatingTask(true);
      await createAdminStorageTask({ action: 'apply', sourceTaskId: task.id });
      Message.success(uiText('已按该扫描结果创建执行任务，正在后台执行'));
      await loadTasks();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('创建执行任务失败')));
    } finally {
      setCreatingTask(false);
    }
  };

  // 迁移不需要扫描：选定原位置与新位置后直接执行。
  const doMigrate = async () => {
    try {
      setCreatingTask(true);
      await createAdminStorageTask({
        action: 'run',
        type: 'migrate',
        sourceBackendId: taskSource || undefined,
        targetBackendId: taskTarget,
      });
      Message.success(uiText('迁移任务已创建，正在后台执行'));
      await loadTasks();
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('创建迁移任务失败')));
    } finally {
      setCreatingTask(false);
    }
  };

  const submitMaint = () => {
    if (taskType === 'migrate') {
      if (!taskTarget) return;
      Modal.confirm({
        title: uiText('确认开始迁移？'),
        content: uiText(
          '会把文件从原位置搬到新位置，搬移成功后删除原位置的对象。'
        ),
        onOk: doMigrate,
      });
      return;
    }
    doScan();
  };

  const confirmApply = (task: TaskItem) => {
    const isOrphan = task.type === 'purge_orphan';
    Modal.confirm({
      title: isOrphan ? uiText('确认清理这些文件？') : uiText('确认执行？'),
      content: isOrphan
        ? uiText('将物理删除清单中的对象，此操作不可恢复。')
        : `${uiText('将按扫描出的清单处理')} ${task.totalItems} ${uiText('个对象')}`,
      okButtonProps: isOrphan ? { status: 'danger' } : undefined,
      onOk: () => doApply(task),
    });
  };

  const openDetail = async (task: TaskItem) => {
    setDetailTask(task);
    setDetailItems([]);
    setDetailLoading(true);
    try {
      const res = await fetchAdminStorageTaskItems(task.id);
      setDetailItems(res.data.items || []);
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('读取任务结果失败')));
    } finally {
      setDetailLoading(false);
    }
  };

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      name: '',
      kind: 'pan123',
      sharePrefer: 'download',
      directPrefer: 'download',
      deliveryPrefer: 'proxy',
      channelSwitch: false,
      directLinkAuth: false,
      directLinkAuthKey: '',
      isEnabled: true,
      isDefault: false,
      parentFileId: undefined,
      endpoint: '',
      region: 'us-east-1',
      bucket: '',
      prefix: '',
      pathStyle: true,
    });
    setVisible(true);
  };

  const openEdit = (item: StorageBackend) => {
    setEditing(item);
    form.setFieldsValue({
      name: item.name,
      kind: item.kind,
      // 默认与后端一致：分享优先自用下载流量、站内直链优先直链流量。
      sharePrefer: item.settings?.share_prefer || 'download',
      directPrefer: item.settings?.direct_prefer || 'download',
      deliveryPrefer: item.settings?.delivery_prefer || 'proxy',
      // 缺省视为关闭（严格模式，与后端默认一致）。
      channelSwitch: item.settings?.channel_switch === true,
      directLinkAuth: Boolean(item.settings?.direct_link_auth),
      // 鉴权密钥不回传浏览器：编辑时留空即沿用已保存的密钥。
      directLinkAuthKey: '',
      isEnabled: item.isEnabled,
      isDefault: item.isDefault,
      parentFileId: item.settings?.parent_file_id,
      endpoint: item.settings?.endpoint || '',
      region: item.settings?.region || 'us-east-1',
      bucket: item.settings?.bucket || '',
      prefix: item.settings?.prefix || '',
      pathStyle: item.settings?.path_style !== false,
    });
    setVisible(true);
  };

  const submit = async () => {
    try {
      const values = await form.validate();
      const settings: Record<string, unknown> = {};
      if (values.kind === 'pan123') {
        settings.parent_file_id = Number(values.parentFileId);
        settings.share_prefer =
          values.sharePrefer === 'direct' ? 'direct' : 'download';
        settings.direct_prefer =
          values.directPrefer === 'direct' ? 'direct' : 'download';
        settings.delivery_prefer =
          values.deliveryPrefer === 'redirect' ? 'redirect' : 'proxy';
        settings.channel_switch = values.channelSwitch === true;
        settings.direct_link_auth = Boolean(values.directLinkAuth);
        // 密钥留空表示沿用已保存的密钥（服务端自己取回），不发送空值覆盖。
        const authKey = String(values.directLinkAuthKey || '').trim();
        if (authKey) {
          settings.direct_link_auth_key = authKey;
        }
      } else if (values.kind === 's3') {
        settings.endpoint = String(values.endpoint || '').trim();
        settings.region = String(values.region || '').trim() || 'us-east-1';
        settings.bucket = String(values.bucket || '').trim();
        settings.prefix = String(values.prefix || '').trim();
        settings.path_style = Boolean(values.pathStyle);
      }
      setSaving(true);
      const res = await saveAdminStorageBackend({
        id: editing?.id || 0,
        name: values.name,
        kind: values.kind,
        settings,
        isEnabled: Boolean(values.isEnabled),
        isDefault: Boolean(values.isDefault),
      });
      setItems(res.data.items || []);
      setVisible(false);
      Message.success(uiText('存储后端已保存并即时生效'));
    } catch (error) {
      if (error?.errorFields) return;
      Message.error(apiErrorMessage(error, uiText('保存存储后端失败')));
    } finally {
      setSaving(false);
    }
  };

  const taskTypeText = (type: string) =>
    type === 'migrate'
      ? uiText('迁移')
      : type === 'encrypt'
      ? uiText('加密')
      : type === 'decrypt'
      ? uiText('解密')
      : type === 'chunk'
      ? uiText('分片')
      : uiText('孤立文件');

  const taskColumns = [
    {
      title: uiText('任务'),
      render: (_: unknown, task: TaskItem) => (
        <Space size={4}>
          <span>{taskTypeText(task.type)}</span>
          <Tag size="small" color={task.phase === 'scan' ? 'gray' : 'arcoblue'}>
            {task.phase === 'scan' ? uiText('扫描') : uiText('执行')}
          </Tag>
        </Space>
      ),
    },
    {
      title: uiText('状态'),
      dataIndex: 'status',
      render: (status: string) => {
        const map: Record<string, { color?: string; text: string }> = {
          queued: { text: uiText('排队中') },
          running: { color: 'arcoblue', text: uiText('执行中') },
          done: { color: 'green', text: uiText('已完成') },
          failed: { color: 'red', text: uiText('失败') },
          canceled: { text: uiText('已取消') },
        };
        const info = map[status] || { text: status };
        return <Tag color={info.color}>{info.text}</Tag>;
      },
    },
    {
      title: uiText('结果'),
      render: (_: unknown, task: TaskItem) =>
        task.phase === 'scan'
          ? `${uiText('命中')} ${task.totalItems}`
          : `${task.doneItems}/${task.totalItems}${
              task.failedItems ? `（${uiText('失败')} ${task.failedItems}）` : ''
            }`,
    },
    {
      title: uiText('创建时间'),
      dataIndex: 'createdAt',
      render: (value: string) => formatTime(value),
    },
    {
      title: uiText('操作'),
      width: 190,
      render: (_: unknown, task: TaskItem) => (
        <Space size={4}>
          <Button size="mini" type="text" onClick={() => openDetail(task)}>
            {uiText('查看结果')}
          </Button>
          {task.phase === 'scan' &&
            task.status === 'done' &&
            task.totalItems > 0 && (
              <Button
                size="mini"
                type="text"
                status={task.type === 'purge_orphan' ? 'danger' : undefined}
                onClick={() => confirmApply(task)}
              >
                {task.type === 'purge_orphan' ? uiText('清理') : uiText('执行')}
              </Button>
            )}
        </Space>
      ),
    },
  ];

  const columns = [
    {
      title: uiText('名称'),
      dataIndex: 'name',
      render: (name: string, item: StorageBackend) => (
        <span>
          {name}{' '}
          {item.isDefault && (
            <Tag size="small" color="arcoblue">
              {uiText('默认')}
            </Tag>
          )}
          {!item.isEnabled && (
            <Tag size="small">{uiText('已停用')}</Tag>
          )}
          {item.isEnabled && !item.ready && (
            <Tag size="small" color="red">
              {uiText('未就绪')}
            </Tag>
          )}
        </span>
      ),
    },
    {
      title: uiText('类型'),
      dataIndex: 'kind',
      render: (kind: string) => kindLabel(kind),
    },
    {
      title: uiText('对象占用'),
      dataIndex: 'usedBytes',
      render: (value: number) => formatBytes(value || 0),
    },
    {
      title: uiText('操作'),
      render: (_: unknown, item: StorageBackend) => (
        <Button size="mini" type="text" onClick={() => openEdit(item)}>
          {uiText('编辑')}
        </Button>
      ),
    },
  ];

  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('存储管理')}
        description={uiText(
          '登记文件存储后端：新上传默认写入「默认」后端；停用后端不会删除已有文件，历史对象仍可正常读取。'
        )}
        extra={
          <>
            <Button icon={<IconRefresh />} onClick={load} loading={loading}>
              {uiText('刷新')}
            </Button>
            <Button type="primary" icon={<IconPlus />} onClick={openCreate}>
              {uiText('新增存储后端')}
            </Button>
          </>
        }
      />
      <Spin loading={loading}>
        <Card>
          <Table
            rowKey="id"
            columns={columns}
            data={items}
            pagination={false}
            border
          />
          <Typography.Text
            type="secondary"
            className={styles['config-description']}
          >
            {pan123Ready
              ? uiText(
                  '123 云盘的 access_token 由服务端自动申请与续期（30 天有效），密钥不会下发到浏览器。'
                )
              : uiText(
                  '当前部署未配置 123 云盘应用凭据（XPH_PAN123_CLIENT_ID / XPH_PAN123_CLIENT_SECRET），无法启用 123 云盘后端。'
                )}
          </Typography.Text>
          <Typography.Text
            type="secondary"
            className={styles['config-description']}
          >
            {s3Ready
              ? uiText(
                  '对象存储支持 S3 兼容协议（AWS S3 / MinIO / Cloudflare R2 / 阿里云 OSS / 腾讯云 COS），访问密钥只保存在部署 .env 中。'
                )
              : uiText(
                  '当前部署未配置对象存储访问凭据（XPH_S3_ACCESS_KEY_ID / XPH_S3_SECRET_ACCESS_KEY），无法启用对象存储后端。'
                )}
          </Typography.Text>
        </Card>

        <Card style={{ marginTop: 16 }}>
          <div className={styles['config-title']}>{uiText('维护任务')}</div>
          <Radio.Group
            type="button"
            value={taskType}
            onChange={(value) => setTaskType(value as MaintKind)}
          >
            <Radio value="orphan">{uiText('孤立文件')}</Radio>
            <Radio value="encrypt">{uiText('加密')}</Radio>
            <Radio value="decrypt">{uiText('解密')}</Radio>
            <Radio value="chunk">{uiText('分片')}</Radio>
            <Radio value="migrate">{uiText('迁移')}</Radio>
          </Radio.Group>
          <Typography.Text
            type="secondary"
            className={styles['config-description']}
          >
            {taskType === 'orphan' &&
              uiText(
                '找出没有被任何文件引用的残留数据；系统不会自动清理，确认清单后手动清理（会真正删除文件）。'
              )}
            {taskType === 'encrypt' &&
              uiText('把明文文件改成加密存储；先扫描出清单，确认后再执行。')}
            {taskType === 'decrypt' &&
              uiText('把加密文件改回明文存储；先扫描出清单，确认后再执行。')}
            {taskType === 'chunk' &&
              uiText(
                '按指定大小重新切分文件；先扫描出不符合的文件，确认后再执行。'
              )}
            {taskType === 'migrate' &&
              uiText('把文件从原位置搬到新位置；搬完后删除原位置的文件。')}
          </Typography.Text>
          <Space wrap style={{ marginTop: 12 }}>
            {(taskType === 'encrypt' ||
              taskType === 'decrypt' ||
              taskType === 'chunk') && (
              <Select
                placeholder={uiText('扫描范围：全部后端')}
                style={{ width: 220 }}
                value={scopeBackend}
                onChange={(value) => setScopeBackend(value)}
                allowClear
              >
                {items.map((item) => (
                  <Select.Option key={item.id} value={item.id}>
                    {item.name}（{kindLabel(item.kind)}）
                  </Select.Option>
                ))}
              </Select>
            )}
            {taskType === 'chunk' && (
              <InputNumber
                min={4}
                max={1024}
                step={4}
                style={{ width: 180 }}
                value={chunkSizeMB}
                onChange={(value) => setChunkSizeMB(Number(value) || 0)}
                suffix={uiText('MiB')}
              />
            )}
            {taskType === 'migrate' && (
              <>
                <Select
                  placeholder={uiText('原位置（留空＝其它全部）')}
                  style={{ width: 220 }}
                  value={taskSource}
                  onChange={(value) => setTaskSource(value)}
                  allowClear
                >
                  {items.map((item) => (
                    <Select.Option key={item.id} value={item.id}>
                      {item.name}（{kindLabel(item.kind)}）
                    </Select.Option>
                  ))}
                </Select>
                <Select
                  placeholder={uiText('新位置')}
                  style={{ width: 220 }}
                  value={taskTarget}
                  onChange={(value) => setTaskTarget(value)}
                >
                  {items
                    .filter((item) => item.isEnabled && item.ready)
                    .map((item) => (
                      <Select.Option key={item.id} value={item.id}>
                        {item.name}（{kindLabel(item.kind)}）
                      </Select.Option>
                    ))}
                </Select>
              </>
            )}
            <Button
              type="primary"
              loading={creatingTask}
              onClick={submitMaint}
              disabled={
                (taskType === 'migrate' && !taskTarget) ||
                (taskType === 'chunk' && chunkSizeMB < 4)
              }
            >
              {taskType === 'migrate' ? uiText('开始迁移') : uiText('开始扫描')}
            </Button>
            <Button onClick={loadTasks} loading={tasksLoading}>
              {uiText('刷新')}
            </Button>
          </Space>
          <Table
            rowKey="id"
            columns={taskColumns}
            data={tasks}
            pagination={false}
            size="small"
            border
            style={{ marginTop: 12 }}
          />
        </Card>

        <Card style={{ marginTop: 16 }}>
          <Space style={{ width: '100%', justifyContent: 'space-between' }}>
            <div>
              <Typography.Title heading={6} style={{ margin: 0 }}>
                {uiText('在途上传')}
              </Typography.Title>
              <Typography.Text type="secondary">
                {uiText('进行中的上传任务会占用临时盘；用户放弃的任务可在此直接取消。')}
              </Typography.Text>
            </div>
            <Button onClick={loadUploads} loading={uploadsLoading}>
              {uiText('刷新')}
            </Button>
          </Space>
          <Table
            rowKey="id"
            size="small"
            border
            style={{ marginTop: 12 }}
            loading={uploadsLoading}
            data={uploads}
            pagination={{ pageSize: 10 }}
            columns={[
              { title: uiText('所有者'), dataIndex: 'ownerName', width: 140 },
              { title: uiText('文件名'), dataIndex: 'filename', width: 240 },
              {
                title: uiText('大小'),
                dataIndex: 'totalSize',
                width: 120,
                render: (value: number) => formatBytes(value || 0),
              },
              { title: uiText('状态'), dataIndex: 'status', width: 120 },
              {
                title: uiText('更新时间'),
                dataIndex: 'updatedAt',
                width: 180,
                render: (value: string) => formatTime(value),
              },
              {
                title: uiText('操作'),
                width: 120,
                render: (_: unknown, item: AdminUploadItem) => (
                  <Button
                    size="mini"
                    status="danger"
                    onClick={() => cancelUpload(item)}
                  >
                    {uiText('取消任务')}
                  </Button>
                ),
              },
            ]}
          />
        </Card>
      </Spin>

      {/* 扫描结果 / 执行明细：确认清单、排查失败项 */}
      <Modal
        title={
          detailTask
            ? `${taskTypeText(detailTask.type)} ${
                detailTask.phase === 'scan'
                  ? uiText('扫描结果')
                  : uiText('执行明细')
              }`
            : ''
        }
        visible={Boolean(detailTask)}
        footer={null}
        onCancel={() => setDetailTask(null)}
        unmountOnExit
        autoFocus={false}
        style={{ width: 760 }}
      >
        <Table
          rowKey="blobId"
          size="small"
          border
          loading={detailLoading}
          data={detailItems}
          pagination={{ pageSize: 10 }}
          columns={[
            {
              title: uiText('文件'),
              dataIndex: 'name',
              render: (name: string) =>
                name || (
                  <Typography.Text type="secondary">
                    {uiText('无引用残留')}
                  </Typography.Text>
                ),
            },
            {
              title: uiText('对象 ID'),
              dataIndex: 'blobId',
              width: 170,
              ellipsis: true,
            },
            {
              title: uiText('大小'),
              dataIndex: 'sizeBytes',
              width: 100,
              render: (value: number) => formatBytes(value || 0),
            },
            {
              title: uiText('状态'),
              dataIndex: 'status',
              width: 100,
              render: (status: string) =>
                status === 'pending' ? (
                  <Tag>{uiText('待处理')}</Tag>
                ) : status === 'skipped' ? (
                  <Tag>{uiText('已跳过')}</Tag>
                ) : status === 'failed' ? (
                  <Tag color="red">{uiText('失败')}</Tag>
                ) : (
                  <Tag color="green">{uiText('已完成')}</Tag>
                ),
            },
            {
              title: uiText('说明'),
              dataIndex: 'error',
              ellipsis: true,
            },
          ]}
        />
      </Modal>

      <Modal
        title={editing ? uiText('编辑存储后端') : uiText('新增存储后端')}
        visible={visible}
        confirmLoading={saving}
        onOk={submit}
        onCancel={() => setVisible(false)}
        unmountOnExit
      >
        <Form form={form} layout="vertical">
          <Form.Item
            label={uiText('名称')}
            field="name"
            rules={[{ required: true, message: uiText('请输入名称') }]}
          >
            <Input maxLength={64} placeholder={uiText('例如：云盘归档')} />
          </Form.Item>
          <Form.Item
            label={uiText('类型')}
            field="kind"
            rules={[{ required: true }]}
          >
            <Radio.Group type="button" disabled={Boolean(editing)}>
              <Radio value="s3">{uiText('对象存储')}</Radio>
              <Radio value="pan123">{uiText('123 云盘')}</Radio>
              <Radio value="local">{uiText('本机存储')}</Radio>
            </Radio.Group>
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(prev, next) => prev.kind !== next.kind}
          >
            {/* arco 的 shouldUpdate 渲染函数签名是 (表单值, store)，不是 antd 的
                (form 实例)：这里必须从第一个参数取 values，写成 ({ getFieldValue })
                会拿到 undefined 并抛 "getFieldValue is not a function"。 */}
            {(values) => {
              const kind = values?.kind;
              if (kind === 'pan123') {
                return (
                  <>
                    <Form.Item
                      label={uiText('存储根目录文件夹 ID')}
                      field="parentFileId"
                      rules={[
                        { required: true, message: uiText('请输入文件夹 ID') },
                      ]}
                      extra={uiText(
                        '在 123 云盘中创建用于存放文件的文件夹，填入其数字 ID（根目录 0 不支持直链）。'
                      )}
                    >
                      <InputNumber min={1} style={{ width: '100%' }} />
                    </Form.Item>
                    <Form.Item
                      label={uiText('分享优先使用')}
                      field="sharePrefer"
                      extra={uiText(
                        '分享页与文件页下载经本机中转时，服务端从 123 取内容消耗哪份额度；是否走 302 由下方「交付方式」单独决定。'
                      )}
                    >
                      <Select style={{ width: '100%' }}>
                        <Select.Option value="download">
                          {uiText('自用下载流量')}
                        </Select.Option>
                        <Select.Option value="direct">
                          {uiText('直链流量')}
                        </Select.Option>
                      </Select>
                    </Form.Item>
                    <Form.Item
                      label={uiText('直链优先使用')}
                      field="directPrefer"
                      extra={uiText(
                        '站内直链 /d/ 始终经本机中转（对外只有一个固定地址、交付可用的明文）；此设置只决定服务端从 123 取内容时消耗哪份额度。选「直链流量」时会自动为 123 云盘开启直链空间（幂等，只开不关）。'
                      )}
                    >
                      <Select style={{ width: '100%' }}>
                        <Select.Option value="direct">
                          {uiText('直链流量')}
                        </Select.Option>
                        <Select.Option value="download">
                          {uiText('自用下载流量')}
                        </Select.Option>
                      </Select>
                    </Form.Item>
                    <Form.Item
                      label={uiText('交付方式')}
                      field="deliveryPrefer"
                      extra={uiText(
                        '优先本机中转（默认）：由服务器转发，用户始终只访问本站地址。优先302连接：明文文件让浏览器直连 123（消耗直链流量），需站点交付策略为 302；选它会自动为 123 云盘开启直链空间（幂等）。加密文件与多分片对象仍走本机中转。'
                      )}
                    >
                      <Select style={{ width: '100%' }}>
                        <Select.Option value="proxy">
                          {uiText('优先本机中转')}
                        </Select.Option>
                        <Select.Option value="redirect">
                          {uiText('优先302连接')}
                        </Select.Option>
                      </Select>
                    </Form.Item>
                    <Form.Item
                      label={uiText('自用与直链可在异常时互相切换')}
                      field="channelSwitch"
                      triggerPropName="checked"
                      extra={uiText(
                        '开启（默认）：所选通道不可用时自动改用另一条通道，下载不中断。关闭：严格使用所选通道（上面两项的「优先」即「始终」），不可用时直接报错，不会消耗另一份额度——便于判断额度是否用完。'
                      )}
                    >
                      <Switch />
                    </Form.Item>
                    <Form.Item
                      label={uiText('启用直链鉴权')}
                      field="directLinkAuth"
                      triggerPropName="checked"
                      extra={uiText(
                        '需先在 123 云盘直链管理的「鉴权管理」中开启并配置同一密钥；开启后下载地址会带签名，防止直链被他人盗用。'
                      )}
                    >
                      <Switch />
                    </Form.Item>
                    <Form.Item
                      noStyle
                      shouldUpdate={(prev, next) =>
                        prev.directLinkAuth !== next.directLinkAuth
                      }
                    >
                      {(values) =>
                        values?.directLinkAuth ? (
                          <Form.Item
                            label={uiText('鉴权密钥')}
                            field="directLinkAuthKey"
                            extra={uiText(
                              '只保存在服务端，不会回显；留空表示沿用已保存的密钥。'
                            )}
                          >
                            <Input.Password
                              placeholder={
                                Boolean(
                                  editing?.settings
                                    ?.direct_link_auth_key_set
                                )
                                  ? uiText('已保存（留空不修改）')
                                  : uiText('粘贴 123 云盘「鉴权管理」中的密钥')
                              }
                            />
                          </Form.Item>
                        ) : null
                      }
                    </Form.Item>
                  </>
                );
              }
              if (kind === 's3') {
                return (
                  <>
                    <Form.Item
                      label={uiText('Endpoint')}
                      field="endpoint"
                      rules={[
                        { required: true, message: uiText('请输入 Endpoint') },
                      ]}
                      extra={uiText(
                        'S3 兼容服务地址，如 https://minio.example.com（不包含 bucket）。'
                      )}
                    >
                      <Input placeholder="https://minio.example.com" />
                    </Form.Item>
                    <Form.Item
                      label={uiText('Bucket')}
                      field="bucket"
                      rules={[
                        { required: true, message: uiText('请输入 Bucket') },
                      ]}
                    >
                      <Input placeholder="xiaoyuposthub" />
                    </Form.Item>
                    <Form.Item label={uiText('Region')} field="region">
                      <Input placeholder="us-east-1" />
                    </Form.Item>
                    <Form.Item
                      label={uiText('对象键前缀')}
                      field="prefix"
                      extra={uiText('可选：与其它系统共用 bucket 时用于隔离目录。')}
                    >
                      <Input placeholder="xph/blobs" />
                    </Form.Item>
                    <Form.Item
                      label={uiText('Path-Style 寻址')}
                      field="pathStyle"
                      triggerPropName="checked"
                      extra={uiText(
                        'MinIO / Cloudflare R2 建议开启；AWS S3 新桶需关闭（virtual-hosted 寻址）。'
                      )}
                    >
                      <Switch />
                    </Form.Item>
                  </>
                );
              }
              return null;
            }}
          </Form.Item>
          <Form.Item
            label={uiText('启用')}
            field="isEnabled"
            triggerPropName="checked"
          >
            <Switch />
          </Form.Item>
          <Form.Item
            label={uiText('设为默认上传目标')}
            field="isDefault"
            triggerPropName="checked"
          >
            <Switch />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
