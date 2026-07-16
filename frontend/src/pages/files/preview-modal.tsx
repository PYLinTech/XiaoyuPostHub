import React, { useEffect, useState } from 'react';
import { Button, Modal, Spin } from '@arco-design/web-react';
import { IconDownload, IconFile } from '@arco-design/web-react/icon';
import SecureFileViewer from '@/components/SecureFileViewer';
import { supportsFilePreview } from '@/utils/filePreview';
import { ResourceItem } from '../storage/shared';
import styles from '../storage/style/index.module.less';
import uiText from '@/utils/uiText';
interface Props {
  resource?: ResourceItem;
  visible: boolean;
  onDownload?: (resource?: ResourceItem) => void | Promise<void>;
  onClose: () => void;
}
export default function PreviewModal({
  resource,
  visible,
  onDownload,
  onClose,
}: Props) {
  const [previewState, setPreviewState] = useState<
    'checking' | 'supported' | 'unsupported'
  >('checking');
  useEffect(() => {
    let active = true;
    setPreviewState('checking');
    if (visible && resource) {
      supportsFilePreview(resource.name)
        .then((supported) => {
          if (active) setPreviewState(supported ? 'supported' : 'unsupported');
        })
        .catch(() => active && setPreviewState('unsupported'));
    }
    return () => {
      active = false;
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
        {resource && previewState === 'supported' && (
          <SecureFileViewer
            key={resource.id}
            url={`/api/resources/${encodeURIComponent(resource.id)}/preview`}
            name={resource.name}
            size={resource.sizeBytes}
            className={styles.viewer}
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
