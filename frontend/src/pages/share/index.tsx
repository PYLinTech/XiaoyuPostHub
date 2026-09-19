import { apiErrorMessage } from '@/api/client';
import React, { useContext, useEffect, useMemo, useRef, useState } from 'react';
import axios from 'axios';

// 公开分享页的地址由运行时的分享/取件标识与后端下发的下载策略拼装，
// 无法固化为接口函数，因此这里保留 axios 直接请求；错误处理仍走统一工具。
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
import { clientKeyHeaders, decodeDelivery } from '@/utils/fileCrypto';
import { DownloadPlan, downloadPlan } from '@/utils/delivery';
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
  /** 逐文件打包时用于前端接收校验的明文哈希。 */
  sha256?: string;
}
interface ShareMetadata {
  name: string;
  kind: 'file' | 'folder';
  sizeBytes: number;
  mimeType?: string;
  /** 单文件分享：文件是否以加密形式存储（分享页展示标识）。 */
  encrypted?: boolean;
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
    shareRetrievalMode?: 'proxy' | 'redirect';
    redirectFallback?: boolean;
    prepareUrl: string;
  };
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
  // 预览请求序号：只有最后一次请求可以落地（防止竞态产生游离的 objectURL）。
  const previewSequence = useRef(0);
  const metadataSequence = useRef(0);
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
  // silent：下载完成后的统计刷新，不进入 loading、不重置目录位置。
  const loadMetadata = async (
    sharePassword = '',
    unlock = false,
    silent = false
  ) => {
    // 竞态守卫：令牌快速变化（或解锁重试）时只允许最后一次请求落地。
    const sequence = (metadataSequence.current += 1);
    if (!silent) {
      unlock ? setUnlocking(true) : setLoading(true);
    }
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
      if (metadataSequence.current !== sequence) return;
      if (response.data.locked && sharePassword) {
        setError(uiText('分享密码错误'));
        return;
      }
      setMetadata(response.data);
      if (silent) return;
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
      if (metadataSequence.current !== sequence) return;
      // 按状态码分类：403=封禁/审核未通过，410=已失效，404=不存在。
      const status = (requestError as { response?: { status?: number } })?.response
        ?.status;
      const serverMessage = (
        requestError as { response?: { data?: { msg?: string } } }
      )?.response?.data?.msg;
      setError(
        serverMessage ||
          (status === 403
            ? uiText('分享文件被封禁或正在审核')
            : status === 410
            ? uiText('分享已失效')
            : uiText('分享不存在或暂时无法访问'))
      );
    } finally {
      // 陈旧请求不应关闭最新请求的 loading。
      if (metadataSequence.current === sequence && !silent) {
        setLoading(false);
        setUnlocking(false);
      }
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
    // 竞态守卫：快速重复点击时只允许最后一次请求落地（避免多余的 objectURL）。
    const sequence = (previewSequence.current += 1);
    setPreviewing(true);
    try {
      const supported = await supportsFilePreview(metadata.name);
      if (previewSequence.current !== sequence) return;
      if (!supported) {
        setPreviewUnsupported(true);
        setPreviewVisible(true);
        return;
      }
      // 加密文件在"服务器实时解密关"时以密文下发，此处现场协商临时密钥并解密。
      const { pair, headers: keyHeaders } = await clientKeyHeaders();
      const response = await axios.get(
        `${apiBase}/${encodeURIComponent(identifier)}/preview`,
        {
          responseType: 'arraybuffer',
          headers: {
            ...(activePassword ? { 'X-Share-Password': activePassword } : {}),
            ...keyHeaders,
          },
        }
      );
      const blob = await decodeDelivery(pair, response.headers, response.data);
      if (previewSequence.current !== sequence) return;
      // 旧 URL 由依赖 previewUrl 的清理 effect 回收，这里不重复 revoke。
      setPreviewUrl(URL.createObjectURL(blob));
      setPreviewUnsupported(false);
      setPreviewVisible(true);
    } catch (error) {
      if (previewSequence.current !== sequence) return;
      // 加载失败与"格式不支持"要区分提示：403/412 等应告知真实原因（审核中、
      // 需要刷新页面），静默降级会误导用户以为文件格式有问题。
      const status = (error as { response?: { status?: number } })?.response?.status;
      // 503：访客权限/配额配置不可用（fail-closed），需展示后端的真实原因。
      if (status === 403 || status === 412 || status === 404 || status === 422 || status === 503) {
        Message.error(apiErrorMessage(error, uiText('预览加载失败')));
      }
      setPreviewUnsupported(true);
      setPreviewVisible(true);
    } finally {
      // 只让最后一次请求关闭 loading：陈旧请求结束不应打断最新请求的进度提示。
      if (previewSequence.current === sequence) setPreviewing(false);
    }
  };
  const download = async () => {
    if (!metadata || metadata.locked) return;
    setDownloading(true);
    setDownloadProgress(0);
    try {
      // 现场生成临时密钥对：服务端用它下发 DEK 信封（加密文件由浏览器解密）。
      const { pair, headers: keyHeaders } = await clientKeyHeaders();
      const response = await axios.post<DownloadPlan>(
        metadata.downloadPolicy.prepareUrl,
        {},
        {
          headers: {
            ...(activePassword ? { 'X-Share-Password': activePassword } : {}),
            ...keyHeaders,
          },
        }
      );
      // 前端接收：逐文件/逐片取数 → 解密 → 合并/打包 → 保存（全部在浏览器完成）。
      await downloadPlan(response.data, pair, setDownloadProgress);
      Message.success(uiText('下载已开始'));
      // 静默刷新用量统计：不整页 loading、不把用户拉回根目录。
      loadMetadata(activePassword, false, true);
    } catch (requestError) {
      Message.error(apiErrorMessage(requestError, uiText('下载失败')));
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
              {/* 标题保持中性：具体原因（封禁 / 失效 / 不存在）已按状态码分类并
                  本地化，直接显示在下方——不对已翻译文案做子串匹配（英文界面
                  下 includes 永远匹配不到）。 */}
              {uiText('无法打开分享')}
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
                    {metadata.kind === 'file' && metadata.encrypted && (
                      <span>
                        <IconLock /> {uiText('该文件已加密存储')}
                      </span>
                    )}
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
