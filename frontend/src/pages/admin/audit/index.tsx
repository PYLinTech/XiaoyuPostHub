import React, { useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import {
  Button,
  Card,
  Input,
  Message,
  Modal,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from '@arco-design/web-react';
import {
  IconCheck,
  IconDownload,
  IconEye,
  IconClose,
} from '@arco-design/web-react/icon';
import SecureFileViewer from '@/components/SecureFileViewer';
import { supportsFilePreview } from '@/utils/filePreview';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
const { Text } = Typography;
const TabPane = Tabs.TabPane;
interface FileReview {
  resourceId: string;
  name: string;
  sizeBytes: number;
  mimeType?: string;
  ownerName: string;
  status: string;
  reason?: string;
  submittedAt: string;
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
const statusTag = (value: string) => {
  const config = {
    pending: ['orange', uiText('待审核')],
    approved: ['green', uiText('已通过')],
    rejected: ['red', uiText('已驳回')],
  }[value] || ['gray', value];
  return <Tag color={config[0]}>{config[1]}</Tag>;
};
const dateTime = (value: string) =>
  new Date(value).toLocaleString('zh-CN', {
    hour12: false,
  });
const fileSize = (value: number) => {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
};
function Audit() {
  const [fileItems, setFileItems] = useState<FileReview[]>([]);
  const [shareItems, setShareItems] = useState<ShareReview[]>([]);
  const [auditItems, setAuditItems] = useState<AuditItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [previewFile, setPreviewFile] = useState<FileReview>();
  const [filePreviewSupported, setFilePreviewSupported] = useState(false);
  const [previewShare, setPreviewShare] = useState<ShareReview>();
  const [rejectTarget, setRejectTarget] = useState<
    | {
        kind: 'files' | 'shares';
        id: string | number;
      }
    | undefined
  >();
  const [rejectReason, setRejectReason] = useState('');
  const loadAll = async () => {
    setLoading(true);
    try {
      const [files, shares, audit] = await Promise.all([
        axios.get('/api/admin/reviews/files'),
        axios.get('/api/admin/reviews/shares'),
        axios.get('/api/admin/audit?limit=100'),
      ]);
      setFileItems(files.data.items || []);
      setShareItems(shares.data.items || []);
      setAuditItems(audit.data.items || []);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('审查数据加载失败'));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    loadAll();
  }, []);
  const review = async (
    kind: 'files' | 'shares',
    id: string | number,
    action: 'approve' | 'reject',
    reason = ''
  ) => {
    if (action === 'reject' && !reason.trim()) {
      Message.warning(uiText('请填写驳回原因'));
      return;
    }
    try {
      await axios.put(`/api/admin/reviews/${kind}/${encodeURIComponent(id)}`, {
        action,
        reason: reason.trim(),
      });
      Message.success(
        action === 'approve' ? uiText('审核已通过') : uiText('审核已驳回')
      );
      setRejectTarget(undefined);
      setRejectReason('');
      await loadAll();
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('审核操作失败'));
    }
  };
  const openFilePreview = async (item: FileReview) => {
    setPreviewFile(item);
    setFilePreviewSupported(await supportsFilePreview(item.name));
  };
  const fileColumns = [
    {
      title: uiText('文件'),
      dataIndex: 'name',
      ellipsis: true,
    },
    {
      title: uiText('上传用户'),
      dataIndex: 'ownerName',
      width: 140,
    },
    {
      title: uiText('大小'),
      dataIndex: 'sizeBytes',
      width: 110,
      render: fileSize,
    },
    {
      title: uiText('状态'),
      dataIndex: 'status',
      width: 100,
      render: statusTag,
    },
    {
      title: uiText('提交时间'),
      dataIndex: 'submittedAt',
      width: 180,
      render: dateTime,
    },
    {
      title: uiText('操作'),
      width: 300,
      fixed: 'right' as const,
      render: (_: unknown, item: FileReview) => (
        <Space wrap>
          <Button
            size="small"
            icon={<IconEye />}
            onClick={() => openFilePreview(item)}
          >
            {uiText('预览')}
          </Button>
          <Button
            size="small"
            icon={<IconDownload />}
            onClick={() =>
              window.open(
                `/api/admin/reviews/files/${encodeURIComponent(
                  item.resourceId
                )}/download`
              )
            }
          >
            {uiText('下载')}
          </Button>
          {item.status === 'pending' && (
            <>
              <Button
                size="small"
                type="primary"
                icon={<IconCheck />}
                onClick={() => review('files', item.resourceId, 'approve')}
              >
                {uiText('通过')}
              </Button>
              <Button
                size="small"
                status="danger"
                icon={<IconClose />}
                onClick={() =>
                  setRejectTarget({
                    kind: 'files',
                    id: item.resourceId,
                  })
                }
              >
                {uiText('驳回')}
              </Button>
            </>
          )}
        </Space>
      ),
    },
  ];
  const shareColumns = [
    {
      title: uiText('分享内容'),
      dataIndex: 'resourceName',
      ellipsis: true,
    },
    {
      title: uiText('创建用户'),
      dataIndex: 'ownerName',
      width: 140,
    },
    {
      title: uiText('格式'),
      dataIndex: 'descriptionFormat',
      width: 100,
      render: (value: string) => (value === 'html' ? 'HTML' : 'Markdown'),
    },
    {
      title: uiText('状态'),
      dataIndex: 'status',
      width: 100,
      render: statusTag,
    },
    {
      title: uiText('提交时间'),
      dataIndex: 'submittedAt',
      width: 180,
      render: dateTime,
    },
    {
      title: uiText('操作'),
      width: 235,
      fixed: 'right' as const,
      render: (_: unknown, item: ShareReview) => (
        <Space wrap>
          <Button
            size="small"
            icon={<IconEye />}
            onClick={() => setPreviewShare(item)}
          >
            {uiText('预览')}
          </Button>
          {item.status === 'pending' && (
            <>
              <Button
                size="small"
                type="primary"
                icon={<IconCheck />}
                onClick={() => review('shares', item.shareId, 'approve')}
              >
                {uiText('通过')}
              </Button>
              <Button
                size="small"
                status="danger"
                icon={<IconClose />}
                onClick={() =>
                  setRejectTarget({
                    kind: 'shares',
                    id: item.shareId,
                  })
                }
              >
                {uiText('驳回')}
              </Button>
            </>
          )}
        </Space>
      ),
    },
  ];
  const auditColumns = [
    {
      title: uiText('时间'),
      dataIndex: 'createdAt',
      width: 190,
      render: dateTime,
    },
    {
      title: uiText('操作者'),
      dataIndex: 'actorName',
      width: 140,
    },
    {
      title: uiText('动作'),
      dataIndex: 'action',
      width: 200,
      render: (value: string) => (
        <Tooltip content={value}>
          <Tag color="arcoblue" className={styles['audit-action']}>
            {value}
          </Tag>
        </Tooltip>
      ),
    },
    {
      title: uiText('对象'),
      render: (_: unknown, record: AuditItem) => (
        <div>
          {record.targetLabel || record.targetType}
          <br />
          <Text type="secondary">{record.targetType}</Text>
        </div>
      ),
    },
    {
      title: uiText('来源 IP'),
      dataIndex: 'clientIp',
      width: 150,
      render: (value: string) => value || '-',
    },
  ];
  const shareHTML = useMemo(() => {
    if (!previewShare) return '';
    const source =
      previewShare.descriptionFormat === 'html'
        ? previewShare.description
        : (marked.parse(previewShare.description, {
            async: false,
          }) as string);
    return DOMPurify.sanitize(source, {
      USE_PROFILES: {
        html: true,
      },
    });
  }, [previewShare]);
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('审查与审计')}
        description={uiText('审查待发布内容，并追踪关键管理操作。')}
      />
      <Card className={styles['table-card']}>
        <Tabs defaultActiveTab="files">
          <TabPane key="files" title={uiText('文件审查')}>
            <Table
              rowKey="resourceId"
              loading={loading}
              columns={fileColumns}
              data={fileItems}
              pagination={{
                pageSize: 15,
                showTotal: true,
              }}
              noDataElement={uiText('暂无文件审查记录')}
              scroll={{
                x: 1050,
              }}
            />
          </TabPane>
          <TabPane key="shares" title={uiText('分享审查')}>
            <Table
              rowKey="shareId"
              loading={loading}
              columns={shareColumns}
              data={shareItems}
              pagination={{
                pageSize: 15,
                showTotal: true,
              }}
              noDataElement={uiText('暂无自定义分享说明审查记录')}
              scroll={{
                x: 980,
              }}
            />
          </TabPane>
          <TabPane key="audit" title={uiText('系统审计')}>
            <Table
              rowKey="id"
              loading={loading}
              columns={auditColumns}
              data={auditItems}
              pagination={{
                pageSize: 15,
                showTotal: true,
              }}
              noDataElement={uiText('暂无系统审计记录')}
              scroll={{
                x: 860,
              }}
            />
          </TabPane>
        </Tabs>
      </Card>

      <Modal
        visible={Boolean(previewFile)}
        title={previewFile?.name || uiText('文件预览')}
        footer={null}
        onCancel={() => setPreviewFile(undefined)}
        unmountOnExit
        style={{
          width: 'min(1480px, 94vw)',
        }}
      >
        <div
          style={{
            height: 'min(72vh, 820px)',
            minHeight: 460,
          }}
        >
          {previewFile && filePreviewSupported ? (
            <SecureFileViewer
              url={`/api/admin/reviews/files/${encodeURIComponent(
                previewFile.resourceId
              )}/preview`}
              name={previewFile.name}
              size={previewFile.sizeBytes}
              onDownload={() =>
                window.open(
                  `/api/admin/reviews/files/${encodeURIComponent(
                    previewFile.resourceId
                  )}/download`
                )
              }
            />
          ) : previewFile ? (
            <div
              style={{
                height: '100%',
                display: 'grid',
                placeContent: 'center',
                gap: 16,
                textAlign: 'center',
              }}
            >
              <Text>{uiText('该格式不支持预览，请下载后查看')}</Text>
              <Button
                type="primary"
                icon={<IconDownload />}
                onClick={() =>
                  window.open(
                    `/api/admin/reviews/files/${encodeURIComponent(
                      previewFile.resourceId
                    )}/download`
                  )
                }
              >
                {uiText('下载文件')}
              </Button>
            </div>
          ) : null}
        </div>
      </Modal>

      <Modal
        visible={Boolean(previewShare)}
        title={uiText('分享说明预览')}
        footer={null}
        onCancel={() => setPreviewShare(undefined)}
        unmountOnExit
      >
        <div
          style={{
            minHeight: 240,
            maxHeight: '65vh',
            overflow: 'auto',
          }}
          dangerouslySetInnerHTML={{
            __html: shareHTML,
          }}
        />
      </Modal>

      <Modal
        visible={Boolean(rejectTarget)}
        title={uiText('驳回审核')}
        okText={uiText('确认驳回')}
        okButtonProps={{
          status: 'danger',
        }}
        onCancel={() => {
          setRejectTarget(undefined);
          setRejectReason('');
        }}
        onOk={() =>
          rejectTarget &&
          review(rejectTarget.kind, rejectTarget.id, 'reject', rejectReason)
        }
      >
        <Input.TextArea
          value={rejectReason}
          onChange={setRejectReason}
          maxLength={500}
          showWordLimit
          autoSize={{
            minRows: 4,
            maxRows: 8,
          }}
          placeholder={uiText('请输入驳回原因')}
        />
      </Modal>
    </div>
  );
}
export default Audit;
