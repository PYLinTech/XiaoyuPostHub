import React, { useEffect, useState } from 'react';
import { apiErrorMessage } from '@/api/client';
import { resourcePreviewUrl } from '@/api/endpoints';
import axios from 'axios';
import { Button, Message, Modal, Spin } from '@arco-design/web-react';
import { IconDownload, IconFile } from '@arco-design/web-react/icon';
import SecureFileViewer from '@/components/SecureFileViewer';
import { supportsFilePreview } from '@/utils/filePreview';
import { clientKeyHeaders, decodeDelivery } from '@/utils/fileCrypto';
import { ResourceItem } from '../storage/shared';
import styles from '../storage/style/index.module.less';
import uiText from '@/utils/uiText';
interface Props {
  resource?: ResourceItem;
  visible: boolean;
  /** 无下载权限时隐藏预览器工具栏的下载按钮。 */
  canDownload?: boolean;
  onDownload?: (resource?: ResourceItem) => void | Promise<void>;
  onClose: () => void;
}
export default function PreviewModal({
  resource,
  visible,
  canDownload = true,
  onDownload,
  onClose,
}: Props) {
  const [previewState, setPreviewState] = useState<
    'checking' | 'supported' | 'unsupported'
  >('checking');
  const [previewBlobUrl, setPreviewBlobUrl] = useState<string>();
  useEffect(() => {
    let active = true;
    let objectUrl: string | undefined;
    setPreviewState('checking');
    setPreviewBlobUrl(undefined);
    if (visible && resource) {
      (async () => {
        try {
          const supported = await supportsFilePreview(resource.name);
          if (!active) return;
          if (!supported) {
            setPreviewState('unsupported');
            return;
          }
          // 与下载共用交付链路：加密文件在"服务器实时解密关"时以密文下发，
          // 此处现场协商临时密钥并解密为 Blob URL 供预览器读取。
          const { pair, headers } = await clientKeyHeaders();
          const response = await axios.get(resourcePreviewUrl(resource.id), {
            responseType: 'arraybuffer',
            headers,
          });
          const blob = await decodeDelivery(pair, response.headers, response.data);
          if (!active) return;
          objectUrl = URL.createObjectURL(blob);
          setPreviewBlobUrl(objectUrl);
          setPreviewState('supported');
        } catch (error) {
          if (!active) return;
          // 区分"格式不支持"与加载失败：403（审核中/无权限）、412（需浏览器
          // 解密）等应告知真实原因，静默降级会让用户误以为文件格式有问题。
          const status = (error as { response?: { status?: number } })?.response?.status;
          if (status === 403 || status === 412 || status === 404 || status === 422) {
            Message.error(apiErrorMessage(error, uiText('预览加载失败')));
          }
          setPreviewState('unsupported');
        }
      })();
    }
    return () => {
      active = false;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [resource, visible]);
  const download = () => {
    onDownload?.(resource);
  };
  return (
    <Modal
      className={styles['preview-modal']}
      visible={visible}
      title={resource?.name || uiText('文件预览')}
      footer={null}
      onCancel={onClose}
      unmountOnExit
      style={{
        width: 'min(1480px, 94vw)',
      }}
    >
      <div className={styles['preview-frame']}>
        {resource && previewState === 'checking' && (
          <Spin className={styles['preview-loading']} />
        )}
        {resource && previewState === 'unsupported' && (
          <div className={styles['preview-unsupported']}>
            <IconFile />
            <span>{uiText('该格式不支持预览，请下载后查看')}</span>
            {onDownload && (
              <Button type="primary" icon={<IconDownload />} onClick={download}>
                {uiText('下载文件')}
              </Button>
            )}
          </div>
        )}
        {resource && previewState === 'supported' && previewBlobUrl && (
          <SecureFileViewer
            key={`${resource.id}-${previewBlobUrl}`}
            url={previewBlobUrl}
            name={resource.name}
            size={resource.sizeBytes}
            className={styles.viewer}
            canDownload={canDownload}
            onDownload={onDownload ? download : undefined}
            onStateChange={(state) => {
              if (state.error) setPreviewState('unsupported');
            }}
          />
        )}
      </div>
    </Modal>
  );
}
