import React, { useContext, useEffect, useMemo, useState } from 'react';
import axios from 'axios';
import DOMPurify from 'dompurify';
import { marked } from 'marked';
import { useParams } from 'react-router-dom';
import {
  Breadcrumb,
  Button,
  Card,
  Input,
  Message,
  Modal,
  Progress,
  Spin,
  Typography,
} from '@arco-design/web-react';
import {
  IconClockCircle,
  IconCloseCircle,
  IconDownload,
  IconEye,
  IconFile,
  IconFolder,
  IconLock,
  IconRight,
  IconUser,
} from '@arco-design/web-react/icon';
import { GlobalContext } from '@/context';
import SecureFileViewer from '@/components/SecureFileViewer';
import logoUrl from '@/assets/logo.svg';
import { supportsFilePreview } from '@/utils/filePreview';
import { formatBytes, formatTime } from '@/utils/format';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
interface ShareTreeItem {
  id: string;
  parentId?: string;
  kind: 'file' | 'folder';
  name: string;
  relativePath: string;
  sizeBytes: number;
  mimeType?: string;
}
interface ShareMetadata {
  name: string;
  kind: 'file' | 'folder';
  sizeBytes: number;
  mimeType?: string;
  passwordRequired: boolean;
  locked: boolean;
  expiresAt?: string;
  downloadCount: number;
  downloadLimit?: number;
  trafficUsedBytes: number;
  trafficLimitBytes?: number;
  description?: string;
  descriptionFormat?: 'markdown' | 'html';
  owner?: {
    username: string;
    avatar?: string;
  };
  items?: ShareTreeItem[];
  downloadPolicy: {
    folderPackMode: 'frontend' | 'backend';
    shareDeliveryMode: 'blob' | 'temporary_link';
    prepareUrl: string;
  };
}
interface PreparedDownload {
  packMode: 'frontend' | 'backend';
  deliveryMode: 'blob' | 'temporary_link';
  url?: string;
  fileName?: string;
  archiveName?: string;
  items?: Array<
    ShareTreeItem & {
      url?: string;
    }
  >;
}
function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = name;
  anchor.click();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
export default function PublicSharePage({ pickupCode }: { pickupCode?: string }) {
  const { token } = useParams<{
    token: string;
  }>();
  const { siteName, siteIconUrl } = useContext(GlobalContext);
  const identifier = pickupCode || token;
  const apiBase = pickupCode ? '/api/pickups' : '/api/shares';
  const [metadata, setMetadata] = useState<ShareMetadata>();
  const [password, setPassword] = useState('');
  const [activePassword, setActivePassword] = useState('');
  const [loading, setLoading] = useState(true);
  const [unlocking, setUnlocking] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [downloadProgress, setDownloadProgress] = useState<number>();
  const [previewing, setPreviewing] = useState(false);
  const [previewVisible, setPreviewVisible] = useState(false);
  const [previewUnsupported, setPreviewUnsupported] = useState(false);
  const [previewUrl, setPreviewUrl] = useState('');
  const [error, setError] = useState('');
  const [folderPath, setFolderPath] = useState<ShareTreeItem[]>([]);
  const descriptionHTML = useMemo(() => {
    if (!metadata?.description) return '';
    const source =
      metadata.descriptionFormat === 'html'
        ? metadata.description
        : (marked.parse(metadata.description, {
            async: false,
          }) as string);
    return DOMPurify.sanitize(source, {
      USE_PROFILES: {
        html: true,
      },
    });
  }, [metadata?.description, metadata?.descriptionFormat]);
  const loadMetadata = async (sharePassword = '', unlock = false) => {
    unlock ? setUnlocking(true) : setLoading(true);
    setError('');
    try {
      const response = await axios.get<ShareMetadata>(
        `${apiBase}/${encodeURIComponent(identifier)}`,
        {
          headers: sharePassword
            ? {
                'X-Share-Password': sharePassword,
              }
            : undefined,
        }
      );
      if (response.data.locked && sharePassword) {
        setError(uiText('分享密码错误'));
        return;
      }
      setMetadata(response.data);
      if (!response.data.locked) {
        setActivePassword(sharePassword);
        const root =
          response.data.kind === 'folder'
            ? response.data.items?.find((item) => !item.parentId)
            : undefined;
        setFolderPath(root ? [root] : []);
      } else {
        setFolderPath([]);
      }
    } catch (requestError) {
      const status = requestError?.response?.status;
      setError(
        requestError?.response?.data?.msg ||
          (status === 410
            ? uiText('分享已失效')
            : uiText('分享不存在或暂时无法访问'))
      );
    } finally {
      setLoading(false);
      setUnlocking(false);
    }
  };
  useEffect(() => {
    loadMetadata();
    // token 变化时重新载入公开分享。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identifier]);
  useEffect(
    () => () => {
      if (previewUrl) URL.revokeObjectURL(previewUrl);
    },
    [previewUrl]
  );
  const preview = async () => {
    if (!metadata || metadata.locked) return;
    if (metadata.kind === 'folder') {
      document.getElementById('share-folder-preview')?.scrollIntoView({
        behavior: 'smooth',
        block: 'start',
      });
      return;
    }
    setPreviewing(true);
    try {
      const supported = await supportsFilePreview(metadata.name);
      if (!supported) {
        setPreviewUnsupported(true);
        setPreviewVisible(true);
        return;
      }
      const response = await axios.get(
        `${apiBase}/${encodeURIComponent(identifier)}/preview`,
        {
          responseType: 'blob',
          headers: activePassword
            ? {
                'X-Share-Password': activePassword,
              }
            : undefined,
        }
      );
      if (previewUrl) URL.revokeObjectURL(previewUrl);
      setPreviewUrl(URL.createObjectURL(response.data));
      setPreviewUnsupported(false);
      setPreviewVisible(true);
    } catch {
      setPreviewUnsupported(true);
      setPreviewVisible(true);
    } finally {
      setPreviewing(false);
    }
  };
  const downloadPreparedArtifact = async (prepared: PreparedDownload) => {
    if (!prepared.url) throw new Error(uiText('下载任务未返回有效地址'));
    const url = new URL(prepared.url, window.location.origin).toString();
    if (prepared.deliveryMode === 'temporary_link') {
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.click();
      return;
    }
    const response = await axios.get(url, {
      responseType: 'blob',
      onDownloadProgress: (event) => {
        if (event.total) {
          setDownloadProgress(Math.round((event.loaded * 100) / event.total));
        }
      },
    });
    saveBlob(response.data, prepared.fileName || metadata?.name || 'download');
  };
  const downloadFrontendArchive = async (prepared: PreparedDownload) => {
    const { default: JSZip } = await import('jszip');
    const zip = new JSZip();
    const items = prepared.items || [];
    const files = items.filter((item) => item.kind === 'file' && item.url);
    items
      .filter((item) => item.kind === 'folder')
      .forEach((item) => zip.folder(item.relativePath));
    for (let index = 0; index < files.length; index += 1) {
      const item = files[index];
      const response = await axios.get(item.url as string, {
        responseType: 'blob',
      });
      zip.file(item.relativePath, response.data);
      setDownloadProgress(Math.round(((index + 1) * 80) / files.length));
    }
    const archive = await zip.generateAsync(
      {
        type: 'blob',
      },
      ({ percent }) => setDownloadProgress(80 + Math.round(percent * 0.2))
    );
    saveBlob(
      archive,
      prepared.archiveName || `${metadata?.name || uiText('分享')}.zip`
    );
  };
  const download = async () => {
    if (!metadata || metadata.locked) return;
    setDownloading(true);
    setDownloadProgress(0);
    try {
      const response = await axios.post<PreparedDownload>(
        metadata.downloadPolicy.prepareUrl,
        {},
        {
          headers: activePassword
            ? {
                'X-Share-Password': activePassword,
              }
            : undefined,
        }
      );
      if (response.data.packMode === 'frontend') {
        await downloadFrontendArchive(response.data);
      } else {
        await downloadPreparedArtifact(response.data);
      }
      Message.success(uiText('下载已开始'));
      loadMetadata(activePassword);
    } catch (requestError) {
      Message.error(
        requestError?.response?.data?.msg || uiText('下载失败，请稍后重试')
      );
    } finally {
      setDownloading(false);
      setDownloadProgress(undefined);
    }
  };
  const unlocked = metadata && !metadata.locked;
  const currentFolder = folderPath[folderPath.length - 1];
  const visibleFolderItems = useMemo(
    () =>
      (metadata?.items || [])
        .filter((item) => item.parentId === currentFolder?.id)
        .sort((left, right) => {
          if (left.kind !== right.kind) return left.kind === 'folder' ? -1 : 1;
          return left.name.localeCompare(right.name, 'zh-CN');
        }),
    [currentFolder?.id, metadata?.items]
  );
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.brand}>
          <img src={siteIconUrl || logoUrl} alt={uiText('站点图标')} />
          <span>{siteName || 'XiaoyuPostHub'}</span>
        </div>
        <span className={styles['header-label']}>{uiText(pickupCode ? '取件码' : '文件分享')}</span>
      </header>

      <main className={styles.main}>
        {loading ? (
          <Spin className={styles.loading} />
        ) : error && !metadata ? (
          <Card className={`${styles['status-card']} ${styles.danger}`}>
            <IconCloseCircle className={styles['status-icon']} />
            <Typography.Title heading={4}>
              {error.includes(uiText('封禁'))
                ? uiText('分享已被封禁')
                : error.includes(uiText('失效')) ||
                  error.includes(uiText('过期'))
                ? uiText('分享已过期')
                : uiText('无法打开分享')}
            </Typography.Title>
            <Typography.Text type="secondary">{error}</Typography.Text>
          </Card>
        ) : (
          metadata && (
            <>
              <Card className={styles.hero}>
                <div className={styles['resource-icon']}>
                  {metadata.kind === 'folder' ? <IconFolder /> : <IconFile />}
                </div>
                <div className={styles['resource-info']}>
                  <Typography.Title heading={4} className={styles.title}>
                    {metadata.name}
                  </Typography.Title>
                  <div className={styles.meta}>
                    <span>
                      {metadata.kind === 'folder'
                        ? unlocked
                          ? `${uiText('共')} ${
                              metadata.items?.length || 0
                            } ${uiText('项')}`
                          : uiText('文件夹')
                        : formatBytes(metadata.sizeBytes)}
                    </span>
                    <span>
                      <IconClockCircle />{' '}
                      {metadata.expiresAt
                        ? `${uiText('到期')} ${formatTime(metadata.expiresAt)}`
                        : uiText('长期有效')}
                    </span>
                    <span>
                      {uiText('已下载')}
                      {metadata.downloadCount}
                      {metadata.downloadLimit == null
                        ? uiText(' 次')
                        : ` / ${metadata.downloadLimit}`}
                    </span>
                    <span>
                      {uiText('已用流量')}
                      {formatBytes(metadata.trafficUsedBytes)}
                      {metadata.trafficLimitBytes == null
                        ? ''
                        : ` / ${formatBytes(metadata.trafficLimitBytes)}`}
                    </span>
                  </div>
                  {metadata.owner && unlocked && (
                    <div className={styles.owner}>
                      <IconUser />
                      {uiText('由')}
                      {metadata.owner.username}
                      {uiText('分享')}
                    </div>
                  )}
                </div>
                {unlocked && (
                  <div className={styles['hero-actions']}>
                    <Button
                      size="large"
                      icon={<IconEye />}
                      loading={previewing}
                      onClick={preview}
                    >
                      {uiText('预览')}
                    </Button>
                    <Button
                      type="primary"
                      size="large"
                      icon={<IconDownload />}
                      loading={downloading}
                      onClick={download}
                    >
                      {uiText('下载')}
                    </Button>
                  </div>
                )}
              </Card>

              {metadata.locked ? (
                <Card className={styles['password-card']}>
                  <IconLock className={styles['lock-icon']} />
                  <Typography.Title heading={5}>
                    {uiText('此分享需要密码')}
                  </Typography.Title>
                  <Typography.Text type="secondary">
                    {uiText('输入分享者提供的密码后查看和下载内容。')}
                  </Typography.Text>
                  <div className={styles['password-form']}>
                    <Input.Password
                      value={password}
                      placeholder={uiText('请输入分享密码')}
                      onChange={setPassword}
                      onPressEnter={() =>
                        password && loadMetadata(password, true)
                      }
                    />
                    <Button
                      type="primary"
                      loading={unlocking}
                      disabled={!password}
                      onClick={() => loadMetadata(password, true)}
                    >
                      {uiText('查看分享')}
                    </Button>
                  </div>
                  {error && (
                    <div className={styles['password-error']}>{error}</div>
                  )}
                </Card>
              ) : (
                <>
                  {downloadProgress != null && (
                    <Card className={styles.progress}>
                      <span>{uiText('正在准备下载')}</span>
                      <Progress percent={downloadProgress} size="small" />
                    </Card>
                  )}

                  {metadata.kind === 'folder' && (
                    <Card id="share-folder-preview" className={styles.content}>
                      <Breadcrumb className={styles['folder-breadcrumb']}>
                        {folderPath.map((item, index) => (
                          <Breadcrumb.Item key={item.id}>
                            <Button
                              type="text"
                              size="mini"
                              onClick={() =>
                                setFolderPath((current) =>
                                  current.slice(0, index + 1)
                                )
                              }
                            >
                              {item.name}
                            </Button>
                          </Breadcrumb.Item>
                        ))}
                      </Breadcrumb>
                      <div className={styles['file-list']}>
                        {visibleFolderItems.length ? (
                          visibleFolderItems.map((item) => (
                            <div
                              className={`${styles['file-row']} ${
                                item.kind === 'folder'
                                  ? styles['folder-row']
                                  : ''
                              }`}
                              key={item.id}
                              onDoubleClick={() =>
                                item.kind === 'folder' &&
                                setFolderPath((current) => [...current, item])
                              }
                            >
                              <div className={styles['file-name']}>
                                {item.kind === 'folder' ? (
                                  <IconFolder />
                                ) : (
                                  <IconFile />
                                )}
                                <span title={item.relativePath}>
                                  {item.name}
                                </span>
                              </div>
                              <span className={styles['file-size']}>
                                {item.kind === 'file'
                                  ? formatBytes(item.sizeBytes)
                                  : uiText('文件夹')}
                              </span>
                              {item.kind === 'folder' && (
                                <Button
                                  type="text"
                                  size="mini"
                                  icon={<IconRight />}
                                  aria-label={`${uiText('打开')} ${item.name}`}
                                  onClick={() =>
                                    setFolderPath((current) => [
                                      ...current,
                                      item,
                                    ])
                                  }
                                />
                              )}
                            </div>
                          ))
                        ) : (
                          <div className={styles.empty}>
                            {uiText('空文件夹')}
                          </div>
                        )}
                      </div>
                    </Card>
                  )}

                  {descriptionHTML && (
                    <Card className={styles.content} title={uiText('分享说明')}>
                      <div
                        className={styles.description}
                        dangerouslySetInnerHTML={{
                          __html: descriptionHTML,
                        }}
                      />
                    </Card>
                  )}
                </>
              )}
            </>
          )
        )}
      </main>

      <Modal
        className={styles['preview-modal']}
        visible={previewVisible}
        title={metadata?.name || uiText('文件预览')}
        footer={null}
        unmountOnExit
        style={{
          width: 'min(1480px, 94vw)',
        }}
        onCancel={() => {
          setPreviewVisible(false);
          setPreviewUnsupported(false);
          setPreviewUrl('');
        }}
      >
        <div className={styles['preview-frame']}>
          {previewUnsupported && (
            <div className={styles['preview-unsupported']}>
              <IconFile />
              <span>{uiText('该格式不支持预览，请下载后查看')}</span>
              <Button
                type="primary"
                icon={<IconDownload />}
                loading={downloading}
                onClick={download}
              >
                {uiText('下载文件')}
              </Button>
            </div>
          )}
          {!previewUnsupported && previewUrl && metadata && (
            <SecureFileViewer
              url={previewUrl}
              name={metadata.name}
              size={metadata.sizeBytes}
              className={styles.viewer}
              onDownload={download}
              onStateChange={(state) => {
                if (state.error) setPreviewUnsupported(true);
              }}
            />
          )}
        </div>
      </Modal>

      <footer className={styles.footer}>
        {siteName || 'XiaoyuPostHub'}
        {uiText('· 安全文件分享')}
      </footer>
    </div>
  );
}
