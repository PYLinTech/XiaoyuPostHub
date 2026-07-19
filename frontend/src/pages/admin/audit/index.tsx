import React, { useContext, useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import {
  Button,
  Card,
  Checkbox,
  Input,
  Message,
  Modal,
  Pagination,
  Radio,
  Space,
  Table,
  Tabs,
  Tag,
  Trigger,
  Typography,
} from '@arco-design/web-react';
import {
  IconCode,
  IconDelete,
  IconDownload,
  IconEye,
  IconSearch,
  IconSettings,
} from '@arco-design/web-react/icon';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
import { formatTime } from '@/utils/format';
import { GlobalContext } from '@/context';

const { Text } = Typography;
const TabPane = Tabs.TabPane;

interface FileReview {
  resourceId: string;
  taskId: string;
  name: string;
  relativePath: string;
  sizeBytes: number;
  mimeType?: string;
  ownerName: string;
  status: string;
  reason?: string;
  deleteFile: boolean;
  blocked: boolean;
  exists: boolean;
  trashedAt?: string;
  submittedAt: string;
  rowKey?: string;
}

interface FileTask {
  id: string;
  ownerName: string;
  uploadedAt: string;
  status: string;
  fileCount: number;
  children: FileReview[];
  rowKey?: string;
}

interface ShareReview {
  shareId: number;
  token: string;
  ownerName: string;
  resourceName: string;
  description: string;
  descriptionFormat: 'markdown' | 'html';
  status: string;
  reason?: string;
  deleteLink: boolean;
  blocked: boolean;
  deletedAt?: string;
  submittedAt: string;
}

interface AuditItem {
  id: number;
  actorName: string;
  action: string;
  targetType: string;
  targetLabel?: string;
  clientIp?: string;
  createdAt: string;
}

type SearchScope = 'id' | 'name' | 'username';
type ReviewKind = 'files' | 'shares';
type FileReviewRow = FileTask | FileReview;

function statusTag(value: string) {
  const config = {
    normal: ['green', uiText('正常')],
    approved: ['green', uiText('正常')],
    pending: ['orange', uiText('待审核')],
    rejected: ['red', uiText('未通过')],
    trashed: ['gray', uiText('已删除')],
    deleted: ['gray', uiText('文件已删除')],
    blocked: ['red', uiText('已拉黑')],
  }[value] || ['gray', value];
  return <Tag color={config[0]}>{config[1]}</Tag>;
}

function SearchBox({
  value,
  scopes,
  onValueChange,
  onScopesChange,
  onSearch,
}: {
  value: string;
  scopes: SearchScope[];
  onValueChange: (value: string) => void;
  onScopesChange: (value: SearchScope[]) => void;
  onSearch: () => void;
}) {
  const popup = (
    <div className={styles['review-search-settings']}>
      <Checkbox.Group
        direction="vertical"
        value={scopes}
        onChange={(value) => onScopesChange(value as SearchScope[])}
      >
        <Checkbox value="id">ID</Checkbox>
        <Checkbox value="name">{uiText('文件名')}</Checkbox>
        <Checkbox value="username">{uiText('用户名')}</Checkbox>
      </Checkbox.Group>
    </div>
  );
  return (
    <div className={styles['review-search-box']}>
      <Input
        value={value}
        onChange={onValueChange}
        onPressEnter={onSearch}
        placeholder={uiText('搜索审核内容')}
      />
      <Trigger trigger="click" position="bl" popup={() => popup}>
        <Button
          type="text"
          icon={<IconSettings />}
          aria-label={uiText('搜索范围')}
        />
      </Trigger>
      <Button
        type="text"
        icon={<IconSearch />}
        aria-label={uiText('搜索')}
        onClick={onSearch}
      />
    </div>
  );
}

function Audit() {
  const { userInfo } = useContext(GlobalContext);
  const adminPermissions = userInfo?.adminPermissions || [];
  const canReviewFiles = Boolean(
    userInfo?.isSuperAdmin || adminPermissions.includes('review_files')
  );
  const canReviewShares = Boolean(
    userInfo?.isSuperAdmin || adminPermissions.includes('review_shares')
  );
  const canReadAudit = Boolean(
    userInfo?.isSuperAdmin || adminPermissions.includes('read_audit_log')
  );
  const [activeTab, setActiveTab] = useState(
    canReviewFiles ? 'files' : canReviewShares ? 'shares' : 'audit'
  );
  const [loading, setLoading] = useState(false);
  const [fileRows, setFileRows] = useState<FileReviewRow[]>([]);
  const [fileTotal, setFileTotal] = useState(0);
  const [filePage, setFilePage] = useState(1);
  const [shareItems, setShareItems] = useState<ShareReview[]>([]);
  const [shareTotal, setShareTotal] = useState(0);
  const [sharePage, setSharePage] = useState(1);
  const [auditItems, setAuditItems] = useState<AuditItem[]>([]);
  const [query, setQuery] = useState('');
  const [appliedQuery, setAppliedQuery] = useState('');
  const [scopes, setScopes] = useState<SearchScope[]>(['id', 'name']);
  const [appliedScopes, setAppliedScopes] = useState<SearchScope[]>([
    'id',
    'name',
  ]);
  const [fileSelection, setFileSelection] = useState<string[]>([]);
  const [shareSelection, setShareSelection] = useState<(string | number)[]>([]);
  const [reviewKind, setReviewKind] = useState<ReviewKind>();
  const [reviewStatus, setReviewStatus] = useState<'approved' | 'rejected'>(
    'approved'
  );
  const [reviewReason, setReviewReason] = useState('');
  const [reviewDelete, setReviewDelete] = useState(false);
  const [reviewBlocked, setReviewBlocked] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [previewVisible, setPreviewVisible] = useState(false);
  const [sourceShares, setSourceShares] = useState<number[]>([]);
  const [trashVisible, setTrashVisible] = useState(false);
  const [trashItems, setTrashItems] = useState<FileReview[]>([]);

  const loadFiles = async (page = filePage) => {
    setLoading(true);
    try {
      const response = await axios.get('/api/admin/reviews/files', {
        params: {
          page,
          pageSize: 20,
          q: appliedQuery,
          scopes: appliedScopes.join(','),
        },
      });
      setFileRows(
        (response.data.items || []).map((task: FileTask) => {
          const children = (task.children || []).map((file) => ({
            ...file,
            rowKey: `file:${file.resourceId}`,
          }));
          if (task.fileCount === 1 && children.length === 1) return children[0];
          return {
            ...task,
            rowKey: `task:${task.id}`,
            children,
          };
        })
      );
      setFileTotal(response.data.total || 0);
      setFilePage(page);
      setFileSelection([]);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('文件审核加载失败'));
    } finally {
      setLoading(false);
    }
  };

  const loadShares = async (page = sharePage) => {
    setLoading(true);
    try {
      const response = await axios.get('/api/admin/reviews/shares', {
        params: {
          page,
          pageSize: 20,
          q: appliedQuery,
          scopes: appliedScopes.join(','),
        },
      });
      setShareItems(response.data.items || []);
      setShareTotal(response.data.total || 0);
      setSharePage(page);
      setShareSelection([]);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('分享审核加载失败'));
    } finally {
      setLoading(false);
    }
  };

  const loadAudit = async () => {
    setLoading(true);
    try {
      const response = await axios.get('/api/admin/audit?limit=100');
      setAuditItems(response.data.items || []);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('审计日志加载失败'));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (activeTab === 'files') loadFiles(1);
    else if (activeTab === 'shares') loadShares(1);
    else loadAudit();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, appliedQuery, appliedScopes]);

  const selectedFiles = useMemo(() => {
    const selected = new Set(fileSelection);
    const output: FileReview[] = [];
    fileRows.forEach((row) => {
      if ('children' in row) {
        const wholeTask = selected.has(`task:${row.id}`);
        row.children.forEach((file) => {
          if (wholeTask || selected.has(`file:${file.resourceId}`))
            output.push(file);
        });
      } else if (selected.has(`file:${row.resourceId}`)) {
        output.push(row);
      }
    });
    return output.filter(
      (item, index, all) =>
        all.findIndex((other) => other.resourceId === item.resourceId) === index
    );
  }, [fileSelection, fileRows]);

  const selectedShares = useMemo(
    () => shareItems.filter((item) => shareSelection.includes(item.shareId)),
    [shareItems, shareSelection]
  );

  const openReview = (kind: ReviewKind) => {
    const selected = kind === 'files' ? selectedFiles : selectedShares;
    if (!selected.length) {
      Message.warning(uiText('请选择审核项目'));
      return;
    }
    const first = selected[0];
    setReviewStatus(first.status === 'rejected' ? 'rejected' : 'approved');
    setReviewReason(first.reason || '');
    setReviewDelete(
      kind === 'files'
        ? Boolean((first as FileReview).deleteFile)
        : Boolean((first as ShareReview).deleteLink)
    );
    setReviewBlocked(Boolean(first.blocked));
    setReviewKind(kind);
  };

  const submitReview = async () => {
    if (!reviewKind) return;
    if (reviewStatus === 'rejected' && !reviewReason.trim()) {
      Message.warning(uiText('请输入审核意见'));
      return;
    }
    setSubmitting(true);
    try {
      const response = await axios.put(`/api/admin/reviews/${reviewKind}`, {
        resourceIds:
          reviewKind === 'files'
            ? selectedFiles.map((item) => item.resourceId)
            : undefined,
        shareIds:
          reviewKind === 'shares'
            ? selectedShares.map((item) => item.shareId)
            : undefined,
        status: reviewStatus,
        reason: reviewReason.trim(),
        delete: reviewStatus === 'rejected' && reviewDelete,
        blocked: reviewStatus === 'rejected' && reviewBlocked,
      });
      setReviewKind(undefined);
      Message.success(uiText('审核已提交'));
      (response.data.warnings || []).forEach((warning: string) =>
        Message.warning(warning)
      );
      if (reviewKind === 'files') await loadFiles();
      else await loadShares();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('审核操作失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const downloadFiles = async () => {
    const ids = selectedFiles
      .filter((item) => item.exists)
      .map((item) => item.resourceId);
    if (!ids.length) {
      Message.warning(uiText('请选择可下载文件'));
      return;
    }
    try {
      const response = await axios.post(
        '/api/admin/reviews/files/download',
        { resourceIds: ids },
        { responseType: 'blob' }
      );
      const disposition = response.headers['content-disposition'] || '';
      const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
      const name = encoded
        ? decodeURIComponent(encoded)
        : ids.length === 1
        ? selectedFiles[0].name
        : uiText('审核文件.zip');
      const url = URL.createObjectURL(response.data);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = name;
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('下载失败'));
    }
  };

  const loadTrash = async () => {
    try {
      const response = await axios.get('/api/admin/reviews/files/trash');
      setTrashItems(response.data.items || []);
      setTrashVisible(true);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('审核回收站加载失败'));
    }
  };

  const deleteTrashItem = async (id: string) => {
    await axios.delete(
      `/api/admin/reviews/files/trash/${encodeURIComponent(id)}`
    );
    setTrashItems((current) =>
      current.filter((item) => item.resourceId !== id)
    );
    loadFiles();
  };

  const fileColumns = [
    {
      title: 'ID',
      width: 210,
      render: (_: unknown, item: FileReviewRow) =>
        'id' in item ? item.id : item.resourceId,
    },
    {
      title: uiText('文件名'),
      render: (_: unknown, item: FileReviewRow) =>
        'children' in item ? (
          <div className={styles['review-file-name-list']}>
            {item.children.map((file) => (
              <span key={file.resourceId}>
                {file.relativePath || file.name}
              </span>
            ))}
          </div>
        ) : (
          item.relativePath || item.name
        ),
    },
    {
      title: uiText('上传者'),
      width: 140,
      render: (_: unknown, item: FileReviewRow) => item.ownerName || '-',
    },
    {
      title: uiText('上传时间'),
      width: 190,
      render: (_: unknown, item: FileReviewRow) =>
        formatTime('uploadedAt' in item ? item.uploadedAt : item.submittedAt),
    },
    {
      title: uiText('状态'),
      width: 120,
      render: (_: unknown, item: FileReviewRow) => statusTag(item.status),
    },
  ];

  const shareColumns = [
    { title: 'ID', dataIndex: 'shareId', width: 110 },
    { title: uiText('分享内容'), dataIndex: 'resourceName', ellipsis: true },
    { title: uiText('分享者'), dataIndex: 'ownerName', width: 150 },
    {
      title: uiText('分享时间'),
      dataIndex: 'submittedAt',
      width: 190,
      render: formatTime,
    },
    {
      title: uiText('状态'),
      dataIndex: 'status',
      width: 120,
      render: (_: string, item: ShareReview) =>
        statusTag(
          item.blocked ? 'blocked' : item.deletedAt ? 'trashed' : item.status
        ),
    },
  ];

  const auditColumns = [
    {
      title: uiText('时间'),
      dataIndex: 'createdAt',
      width: 190,
      render: formatTime,
    },
    { title: uiText('操作者'), dataIndex: 'actorName', width: 150 },
    { title: uiText('动作'), dataIndex: 'action', width: 220 },
    {
      title: uiText('对象'),
      render: (_: unknown, item: AuditItem) =>
        item.targetLabel || item.targetType,
    },
    {
      title: uiText('来源 IP'),
      dataIndex: 'clientIp',
      width: 150,
      render: (value: string) => value || '-',
    },
  ];

  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('审查与审计')}
        description={uiText('审核所有用户上传的文件和分享内容。')}
      />
      <Card className={styles['table-card']}>
        <Tabs activeTab={activeTab} onChange={setActiveTab}>
          {canReviewFiles && (
            <TabPane key="files" title={uiText('文件审查')}>
            <div className={styles['review-toolbar']}>
              <SearchBox
                value={query}
                scopes={scopes}
                onValueChange={setQuery}
                onScopesChange={setScopes}
                onSearch={() => {
                  setFilePage(1);
                  setAppliedQuery(query.trim());
                  setAppliedScopes([...scopes]);
                }}
              />
              <Space wrap>
                <Button icon={<IconDownload />} onClick={downloadFiles}>
                  {uiText('下载')}
                </Button>
                <Button type="primary" onClick={() => openReview('files')}>
                  {uiText('审核')}
                </Button>
                <Button
                  status="danger"
                  icon={<IconDelete />}
                  onClick={loadTrash}
                >
                  {uiText('回收站')}
                </Button>
              </Space>
            </div>
            <Table
              rowKey="rowKey"
              loading={loading}
              columns={fileColumns}
              data={fileRows}
              pagination={false}
              rowSelection={{
                type: 'checkbox',
                selectedRowKeys: fileSelection,
                checkStrictly: false,
                onChange: (keys) => setFileSelection(keys.map(String)),
              }}
              noDataElement={uiText('暂无文件审核记录')}
              scroll={{ x: 950 }}
            />
            <Pagination
              current={filePage}
              pageSize={20}
              total={fileTotal}
              onChange={loadFiles}
              className={styles['review-pagination']}
            />
            </TabPane>
          )}
          {canReviewShares && (
            <TabPane key="shares" title={uiText('分享审查')}>
            <div className={styles['review-toolbar']}>
              <SearchBox
                value={query}
                scopes={scopes}
                onValueChange={setQuery}
                onScopesChange={setScopes}
                onSearch={() => {
                  setSharePage(1);
                  setAppliedQuery(query.trim());
                  setAppliedScopes([...scopes]);
                }}
              />
              <Space wrap>
                <Button
                  icon={<IconEye />}
                  onClick={() =>
                    selectedShares.length
                      ? setPreviewVisible(true)
                      : Message.warning(uiText('请选择审核项目'))
                  }
                >
                  {uiText('预览')}
                </Button>
                <Button type="primary" onClick={() => openReview('shares')}>
                  {uiText('审核')}
                </Button>
              </Space>
            </div>
            <Table
              rowKey="shareId"
              loading={loading}
              columns={shareColumns}
              data={shareItems}
              pagination={false}
              rowSelection={{
                type: 'checkbox',
                selectedRowKeys: shareSelection,
                onChange: setShareSelection,
              }}
              noDataElement={uiText('暂无分享审核记录')}
              scroll={{ x: 850 }}
            />
            <Pagination
              current={sharePage}
              pageSize={20}
              total={shareTotal}
              onChange={loadShares}
              className={styles['review-pagination']}
            />
            </TabPane>
          )}
          {canReadAudit && (
            <TabPane key="audit" title={uiText('系统审计')}>
            <Table
              rowKey="id"
              loading={loading}
              columns={auditColumns}
              data={auditItems}
              pagination={{ pageSize: 20 }}
            />
            </TabPane>
          )}
        </Tabs>
      </Card>

      <Modal
        visible={Boolean(reviewKind)}
        title={reviewKind === 'files' ? uiText('文件审核') : uiText('分享审核')}
        onCancel={() => setReviewKind(undefined)}
        onOk={submitReview}
        confirmLoading={submitting}
        okText={uiText('提交')}
        unmountOnExit
      >
        <div className={styles['review-form']}>
          <div>
            <Text>{uiText('审核状态')}：</Text>
            <Radio.Group
              type="button"
              value={reviewStatus}
              onChange={setReviewStatus}
            >
              <Radio value="approved">{uiText('通过')}</Radio>
              <Radio value="rejected">{uiText('不通过')}</Radio>
            </Radio.Group>
          </div>
          {reviewStatus === 'rejected' && (
            <>
              <div>
                <Text>{uiText('审核意见')}：</Text>
                <Input.TextArea
                  value={reviewReason}
                  onChange={setReviewReason}
                  maxLength={100}
                  showWordLimit
                  autoSize={{ minRows: 3, maxRows: 6 }}
                />
              </div>
              <div>
                <Text>{uiText('审核操作')}：</Text>
                <Space direction="vertical">
                  <Checkbox checked={reviewDelete} onChange={setReviewDelete}>
                    {reviewKind === 'files'
                      ? uiText('删除文件')
                      : uiText('删除链接')}
                  </Checkbox>
                  <Checkbox checked={reviewBlocked} onChange={setReviewBlocked}>
                    {reviewKind === 'files'
                      ? uiText('拉黑文件')
                      : uiText('封禁链接')}
                  </Checkbox>
                </Space>
              </div>
            </>
          )}
        </div>
      </Modal>

      <Modal
        visible={previewVisible}
        title={uiText('分享预览')}
        footer={null}
        onCancel={() => setPreviewVisible(false)}
        style={{ width: 'min(980px, 94vw)' }}
        unmountOnExit
      >
        <div className={styles['share-preview-list']}>
          {selectedShares.map((item) => {
            const source = sourceShares.includes(item.shareId);
            const html =
              item.descriptionFormat === 'html'
                ? item.description
                : (marked.parse(item.description || '', {
                    async: false,
                  }) as string);
            return (
              <Card
                key={item.shareId}
                className={styles['share-preview-card']}
                title={
                  <Space>
                    <Checkbox
                      checked={shareSelection.includes(item.shareId)}
                      onChange={(checked) =>
                        setShareSelection((current) =>
                          checked
                            ? [...current, item.shareId]
                            : current.filter((id) => id !== item.shareId)
                        )
                      }
                    />
                    <Text>
                      #{item.shareId} · {item.ownerName} ·{' '}
                      {formatTime(item.submittedAt)}
                    </Text>
                  </Space>
                }
              >
                {source ? (
                  <pre>{item.description}</pre>
                ) : (
                  <div
                    dangerouslySetInnerHTML={{
                      __html: DOMPurify.sanitize(html, {
                        USE_PROFILES: { html: true },
                      }),
                    }}
                  />
                )}
                <Button
                  shape="circle"
                  className={styles['share-source-toggle']}
                  icon={<IconCode />}
                  onClick={() =>
                    setSourceShares((current) =>
                      source
                        ? current.filter((id) => id !== item.shareId)
                        : [...current, item.shareId]
                    )
                  }
                />
              </Card>
            );
          })}
        </div>
      </Modal>

      <Modal
        visible={trashVisible}
        title={uiText('审核回收站')}
        footer={null}
        onCancel={() => setTrashVisible(false)}
        style={{ width: 'min(900px, 94vw)' }}
      >
        <div className={styles['review-toolbar']}>
          <Text>{uiText('这里只允许永久删除，不支持恢复。')}</Text>
          <Button
            status="danger"
            disabled={!trashItems.length}
            onClick={async () => {
              await axios.delete('/api/admin/reviews/files/trash');
              setTrashItems([]);
              loadFiles();
            }}
          >
            {uiText('清空回收站')}
          </Button>
        </div>
        <Table
          rowKey="resourceId"
          data={trashItems}
          pagination={false}
          columns={[
            { title: 'ID', dataIndex: 'resourceId' },
            { title: uiText('文件名'), dataIndex: 'name' },
            { title: uiText('上传者'), dataIndex: 'ownerName' },
            {
              title: uiText('删除时间'),
              dataIndex: 'trashedAt',
              render: formatTime,
            },
            {
              title: uiText('操作'),
              render: (_: unknown, item: FileReview) => (
                <Button
                  size="small"
                  status="danger"
                  onClick={() => deleteTrashItem(item.resourceId)}
                >
                  {uiText('永久删除')}
                </Button>
              ),
            },
          ]}
        />
      </Modal>
    </div>
  );
}

export default Audit;
