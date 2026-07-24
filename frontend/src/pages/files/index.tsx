import React, {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import axios from 'axios';
import {
  Breadcrumb,
  Button,
  Card,
  Input,
  Message,
  Modal,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from '@arco-design/web-react';
import {
  IconDelete,
  IconDownload,
  IconEdit,
  IconEye,
  IconFolder,
  IconFolderAdd,
  IconLink,
  IconShareAlt,
  IconUpload,
} from '@arco-design/web-react/icon';
import LinkModal from './link-modal';
import PreviewModal from './preview-modal';
import {
  formatBytes,
  formatTime,
  ResourceIcon,
  ResourceItem,
} from '../storage/shared';
import styles from '../storage/style/index.module.less';
import uiText from '@/utils/uiText';
import { GlobalContext } from '@/context';
import { useUploadManager } from '@/components/UploadManager';
interface PathItem {
  id?: string;
  name: string;
}
function downloadName(contentDisposition: string, fallback: string) {
  const encoded = contentDisposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
  if (encoded) {
    try {
      return decodeURIComponent(encoded);
    } catch {
      return fallback;
    }
  }
  return contentDisposition.match(/filename="?([^";]+)"?/i)?.[1] || fallback;
}
function saveBlob(blob: Blob, name: string) {
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = name;
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1000);
}
async function responseErrorMessage(error: unknown, fallback: string) {
  if (!axios.isAxiosError(error)) return fallback;
  const data = error.response?.data;
  if (data instanceof Blob) {
    try {
      const parsed = JSON.parse(await data.text());
      return parsed?.msg || fallback;
    } catch {
      return fallback;
    }
  }
  return data?.msg || fallback;
}
export default function FilesPage() {
  const { userInfo } = useContext(GlobalContext);
  const { addFiles } = useUploadManager();
  const permissions = userInfo?.permissions || [];
  const can = (permission: string) => permissions.includes(permission);
  const canUpload = permissions.includes('upload');
  const [items, setItems] = useState<ResourceItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<string[]>([]);
  const [path, setPath] = useState<PathItem[]>([
    {
      name: uiText('全部文件'),
    },
  ]);
  const [folderVisible, setFolderVisible] = useState(false);
  const [folderName, setFolderName] = useState('');
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [renameTarget, setRenameTarget] = useState<ResourceItem>();
  const [renameName, setRenameName] = useState('');
  const [renaming, setRenaming] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [linkMode, setLinkMode] = useState<'share' | 'direct'>('share');
  const [linkVisible, setLinkVisible] = useState(false);
  const [previewResource, setPreviewResource] = useState<ResourceItem>();
  const fileInput = useRef<HTMLInputElement>();
  const parentId = path[path.length - 1]?.id;
  const uploadPath = `/${path.slice(1).map((item) => item.name).join('/')}`;
  const load = useCallback(() => {
    setLoading(true);
    return axios
      .get('/api/resources', {
        params: parentId
          ? {
              parentId,
            }
          : {},
      })
      .then((response) => setItems(response.data.items || []))
      .catch((error) =>
        Message.error(error?.response?.data?.msg || uiText('文件列表加载失败'))
      )
      .finally(() => setLoading(false));
  }, [parentId]);
  useEffect(() => {
    setSelectedKeys([]);
    load();
  }, [load]);
  useEffect(() => {
    const handleCompleted = (event: Event) => {
      const completedParent =
        (event as CustomEvent).detail?.parentId || undefined;
      if (completedParent === parentId) load();
    };
    window.addEventListener('xph-upload-completed', handleCompleted);
    return () =>
      window.removeEventListener('xph-upload-completed', handleCompleted);
  }, [load, parentId]);
  const selectedItems = useMemo(
    () => items.filter((item) => selectedKeys.includes(item.id)),
    [items, selectedKeys]
  );
  const openResource = (item: ResourceItem) => {
    if (item.kind === 'folder') {
      setPath((current) => [
        ...current,
        {
          id: item.id,
          name: item.name,
        },
      ]);
    } else {
      if (!can('preview')) {
        Message.warning(uiText('当前用户组未授予预览权限'));
        return;
      }
      setPreviewResource(item);
    }
  };
  const openGenerator = (mode: 'share' | 'direct') => {
    if (!selectedItems.length) {
      Message.warning(uiText('请至少选择一项内容'));
      return;
    }
    if (
      mode === 'direct' &&
      (selectedItems.length !== 1 || selectedItems[0].kind !== 'file')
    ) {
      Message.warning(uiText('直链仅支持单个文件'));
      return;
    }
    setLinkMode(mode);
    setLinkVisible(true);
  };
  const downloadResources = async (resources: ResourceItem[]) => {
    if (!resources.length) {
      Message.warning(uiText('请至少选择一项内容'));
      return;
    }
    setDownloading(true);
    try {
      const response = await axios.post(
        '/api/resources',
        {
          resourceIds: resources.map((item) => item.id),
        },
        {
          responseType: 'blob',
        }
      );
      const fallback =
        resources.length === 1 && resources[0].kind === 'file'
          ? resources[0].name
          : uiText('下载文件.zip');
      saveBlob(
        response.data,
        downloadName(response.headers['content-disposition'] || '', fallback)
      );
    } catch (error) {
      Message.error(await responseErrorMessage(error, uiText('下载失败')));
    } finally {
      setDownloading(false);
    }
  };
  const downloadResource = (item?: ResourceItem) =>
    downloadResources(item ? [item] : []);
  const createFolder = async () => {
    if (!folderName.trim()) {
      Message.warning(uiText('请输入文件夹名称'));
      return;
    }
    setCreatingFolder(true);
    try {
      await axios.post('/api/resources/folders', {
        name: folderName.trim(),
        parentId: parentId || null,
      });
      setFolderVisible(false);
      setFolderName('');
      Message.success(uiText('文件夹已创建'));
      load();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('创建文件夹失败'));
    } finally {
      setCreatingFolder(false);
    }
  };
  const upload = useCallback(async (fileList?: FileList | null) => {
    const files = Array.from(fileList || []);
    if (!files.length) return;
    await addFiles(files, parentId, uploadPath);
    if (fileInput.current) fileInput.current.value = '';
  }, [addFiles, parentId, uploadPath]);
  useEffect(() => {
    const containsFiles = (event: DragEvent) =>
      Array.from(event.dataTransfer?.types || []).includes('Files');
    const handleDragOver = (event: DragEvent) => {
      if (!containsFiles(event)) return;
      event.preventDefault();
      if (event.dataTransfer) event.dataTransfer.dropEffect = canUpload ? 'copy' : 'none';
    };
    const handleDrop = (event: DragEvent) => {
      if (!containsFiles(event)) return;
      event.preventDefault();
      if (!canUpload) {
        Message.warning(uiText('当前用户组未授予上传权限'));
        return;
      }
      upload(event.dataTransfer?.files);
    };
    window.addEventListener('dragover', handleDragOver);
    window.addEventListener('drop', handleDrop);
    return () => {
      window.removeEventListener('dragover', handleDragOver);
      window.removeEventListener('drop', handleDrop);
    };
  }, [canUpload, upload]);
  const renameResource = async () => {
    if (!renameTarget || !renameName.trim()) {
      Message.warning(uiText('请输入新名称'));
      return;
    }
    setRenaming(true);
    try {
      await axios.put(`/api/resources/${renameTarget.id}`, {
        name: renameName.trim(),
      });
      setRenameTarget(undefined);
      setRenameName('');
      setSelectedKeys([]);
      Message.success(uiText('重命名完成'));
      await load();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('重命名失败'));
    } finally {
      setRenaming(false);
    }
  };
  const removeSelected = () => {
    if (!selectedItems.length) {
      Message.warning(uiText('请先选择要删除的内容'));
      return;
    }
    Modal.confirm({
      title: `${uiText('删除内容')}：${selectedItems.length}`,
      content: uiText('所选内容将移入回收站，可在回收期限内恢复。'),
      okButtonProps: {
        status: 'danger',
      },
      onOk: async () => {
        try {
          await Promise.all(
            selectedItems.map((item) =>
              axios.delete(`/api/resources/${item.id}`)
            )
          );
          Message.success(uiText('已移入回收站'));
          setSelectedKeys([]);
          await load();
        } catch (error) {
          Message.error(error?.response?.data?.msg || uiText('删除失败'));
          await load();
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
          <div className={styles['resource-main']}>
            <Button
              className={styles['name-button']}
              type="text"
              onClick={() => openResource(item)}
            >
              {item.name}
            </Button>
            <div className={styles['mobile-resource-meta']}>
              <span>
                {item.kind === 'folder'
                  ? uiText('文件夹')
                  : `${uiText('文件')} · ${formatBytes(item.sizeBytes)}`}
              </span>
              {item.kind === 'file' &&
                item.reviewStatus &&
                item.reviewStatus !== 'approved' && (
                  <span className={styles[`review-${item.reviewStatus}`]}>
                    {item.reviewStatus === 'pending'
                      ? uiText('待审核')
                      : uiText('已驳回')}
                  </span>
                )}
              <span>{formatTime(item.updatedAt)}</span>
            </div>
          </div>
        </div>
      ),
    },
    {
      title: uiText('大小'),
      dataIndex: 'sizeBytes',
      width: 140,
      className: styles['mobile-hidden'],
      render: (value, item) =>
        item.kind === 'folder' ? '-' : formatBytes(value),
    },
    {
      title: uiText('类型'),
      dataIndex: 'kind',
      width: 130,
      className: styles['mobile-hidden'],
      render: (value) =>
        value === 'folder' ? uiText('文件夹') : uiText('文件'),
    },
    {
      title: uiText('审核状态'),
      dataIndex: 'reviewStatus',
      width: 120,
      className: styles['mobile-hidden'],
      render: (value, item: ResourceItem) => {
        if (item.kind === 'folder' || !value || value === 'approved')
          return '-';
        const tag = (
          <Tag color={value === 'pending' ? 'orange' : 'red'}>
            {value === 'pending' ? uiText('待审核') : uiText('已驳回')}
          </Tag>
        );
        return item.reviewReason ? (
          <Tooltip content={item.reviewReason}>{tag}</Tooltip>
        ) : (
          tag
        );
      },
    },
    {
      title: uiText('更新时间'),
      dataIndex: 'updatedAt',
      width: 210,
      className: styles['mobile-hidden'],
      render: formatTime,
    },
  ];
  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <Typography.Title heading={4} className={styles.title}>
            {uiText('文件')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {uiText('管理你的文件和文件夹。')}
          </Typography.Text>
        </div>
      </div>
      <Card className={styles.workspace}>
        <div className={styles.toolbar}>
          <div className={styles['toolbar-left']}>
            <Button
              type="primary"
              icon={<IconUpload />}
              disabled={!can('upload')}
              onClick={() => fileInput.current?.click()}
            >
              {uiText('上传')}
            </Button>
            <Button
              icon={<IconFolderAdd />}
              disabled={!can('upload')}
              onClick={() => setFolderVisible(true)}
            >
              {uiText('新建文件夹')}
            </Button>
            <Button
              icon={<IconEye />}
              disabled={
                !can('preview') ||
                selectedItems.length !== 1 ||
                selectedItems[0]?.kind !== 'file'
              }
              onClick={() => setPreviewResource(selectedItems[0])}
            >
              {uiText('预览')}
            </Button>
            <Button
              icon={<IconEdit />}
              disabled={!can('rename') || selectedItems.length !== 1}
              onClick={() => {
                setRenameTarget(selectedItems[0]);
                setRenameName(selectedItems[0]?.name || '');
              }}
            >
              {uiText('重命名')}
            </Button>
            <Button
              icon={<IconDownload />}
              loading={downloading}
              disabled={!can('download') || !selectedItems.length}
              onClick={() => downloadResources(selectedItems)}
            >
              {uiText('下载')}
            </Button>
            {(can('share') || can('pickup_share')) && (
              <Button
                icon={<IconShareAlt />}
                disabled={!selectedItems.length}
                onClick={() => openGenerator('share')}
              >
                {uiText('分享')}
              </Button>
            )}
            {can('direct_link') && (
              <Button
                icon={<IconLink />}
                disabled={selectedItems.length !== 1 || selectedItems[0]?.kind !== 'file'}
                onClick={() => openGenerator('direct')}
              >
                {uiText('直链')}
              </Button>
            )}
            <Button
              status="danger"
              icon={<IconDelete />}
              disabled={!can('delete_own') || !selectedItems.length}
              onClick={removeSelected}
            >
              {uiText('删除')}
            </Button>
            <input
              ref={fileInput}
              type="file"
              multiple
              hidden
              onChange={(event) => upload(event.target.files)}
            />
          </div>
          <Typography.Text type="secondary">
            {selectedItems.length
              ? `${uiText('已选择')} ${selectedItems.length} ${uiText('项')}`
              : `${uiText('共')} ${items.length} ${uiText('项')}`}
          </Typography.Text>
        </div>
        <div className={styles.crumbs}>
          <Breadcrumb>
            {path.map((item, index) => (
              <Breadcrumb.Item key={item.id || 'root'}>
                <Button
                  className={styles['crumb-button']}
                  type="text"
                  size="mini"
                  onClick={() =>
                    setPath((current) => current.slice(0, index + 1))
                  }
                >
                  <IconFolder />
                  {item.name}
                </Button>
              </Breadcrumb.Item>
            ))}
          </Breadcrumb>
        </div>
        <Table
          rowKey="id"
          loading={loading}
          columns={columns}
          data={items}
          pagination={false}
          rowSelection={{
            type: 'checkbox',
            selectedRowKeys: selectedKeys,
            onChange: (keys) => setSelectedKeys(keys.map(String)),
          }}
          noDataElement={uiText('当前文件夹为空')}
          onRow={(item) => ({
            onDoubleClick: () => openResource(item),
          })}
        />
      </Card>
      <Modal
        className={styles['compact-modal']}
        visible={folderVisible}
        title={uiText('新建文件夹')}
        onCancel={() => {
          setFolderVisible(false);
          setFolderName('');
        }}
        onOk={createFolder}
        confirmLoading={creatingFolder}
        okText={uiText('创建')}
        unmountOnExit
        style={{
          width: 'min(520px, calc(100vw - 24px))',
        }}
      >
        <Space
          direction="vertical"
          style={{
            width: '100%',
          }}
        >
          <Typography.Text>{uiText('文件夹名称')}</Typography.Text>
          <Input
            maxLength={255}
            value={folderName}
            onChange={setFolderName}
            placeholder={uiText('请输入文件夹名称')}
            onPressEnter={createFolder}
          />
        </Space>
      </Modal>
      <LinkModal
        mode={linkMode}
        resources={selectedItems}
        visible={linkVisible}
        allowLinkShare={can('share')}
        allowPickupShare={can('pickup_share')}
        onClose={() => setLinkVisible(false)}
      />
      <Modal
        className={styles['compact-modal']}
        visible={Boolean(renameTarget)}
        title={uiText('重命名')}
        onCancel={() => {
          setRenameTarget(undefined);
          setRenameName('');
        }}
        onOk={renameResource}
        confirmLoading={renaming}
        okText={uiText('保存')}
        unmountOnExit
        style={{
          width: 'min(520px, calc(100vw - 24px))',
        }}
      >
        <Input
          autoFocus
          maxLength={255}
          value={renameName}
          onChange={setRenameName}
          placeholder={uiText('请输入新名称')}
          onPressEnter={renameResource}
        />
      </Modal>
      <PreviewModal
        resource={previewResource}
        visible={Boolean(previewResource)}
        onDownload={can('download') ? downloadResource : undefined}
        onClose={() => setPreviewResource(undefined)}
      />
    </div>
  );
}
