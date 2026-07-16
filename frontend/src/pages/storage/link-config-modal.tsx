import React, { useEffect, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Checkbox,
  Input,
  InputNumber,
  Message,
  Modal,
  Radio,
  Select,
  Typography,
} from '@arco-design/web-react';
import { IconCheckCircle, IconCopy } from '@arco-design/web-react/icon';
import { ResourceItem } from './shared';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
import writeClipboard from '@/utils/clipboard';
export interface ConfigurableLinkItem {
  id: number;
  resource: ResourceItem;
  expiresAt?: string;
  downloadLimit?: number;
  trafficLimitBytes?: number;
  showOwner?: boolean;
  description?: string;
  descriptionFormat?: 'markdown' | 'html';
}
interface Props {
  mode: 'share' | 'direct';
  item?: ConfigurableLinkItem;
  visible: boolean;
  onClose: () => void;
  onSaved: () => void;
}
const expiryOptions = () => [
  {
    label: uiText('保持当前有效期'),
    value: 'keep',
  },
  {
    label: uiText('从现在起 1 天'),
    value: '86400',
  },
  {
    label: uiText('从现在起 7 天'),
    value: '604800',
  },
  {
    label: uiText('从现在起 30 天'),
    value: '2592000',
  },
  {
    label: uiText('永久有效'),
    value: '0',
  },
];
export default function LinkConfigModal({
  mode,
  item,
  visible,
  onClose,
  onSaved,
}: Props) {
  const [expiry, setExpiry] = useState('keep');
  const [downloadLimit, setDownloadLimit] = useState<number>();
  const [trafficGB, setTrafficGB] = useState<number>();
  const [passwordMode, setPasswordMode] = useState('keep');
  const [password, setPassword] = useState('');
  const [showOwner, setShowOwner] = useState(false);
  const [descriptionFormat, setDescriptionFormat] = useState('markdown');
  const [description, setDescription] = useState('');
  const [loading, setLoading] = useState(false);
  const [generatedPassword, setGeneratedPassword] = useState('');
  useEffect(() => {
    if (!visible || !item) return;
    setExpiry('keep');
    setDownloadLimit(item.downloadLimit);
    setTrafficGB(
      item.trafficLimitBytes == null
        ? undefined
        : item.trafficLimitBytes / 1024 / 1024 / 1024
    );
    setPasswordMode('keep');
    setPassword('');
    setShowOwner(Boolean(item.showOwner));
    setDescriptionFormat(item.descriptionFormat || 'markdown');
    setDescription(item.description || '');
    setGeneratedPassword('');
  }, [item, visible]);
  const save = async () => {
    if (!item) return;
    if (mode === 'share' && passwordMode === 'custom' && !password) {
      Message.warning(uiText('请输入分享密码'));
      return;
    }
    setLoading(true);
    try {
      const common = {
        ...(expiry === 'keep'
          ? {}
          : {
              expiresInSeconds: Number(expiry),
            }),
        downloadLimit: downloadLimit ?? null,
        trafficLimitBytes:
          trafficGB == null ? null : Math.round(trafficGB * 1024 ** 3),
      };
      const response = await axios.put(
        `/api/${mode === 'share' ? 'shares' : 'direct-links'}/manage/${
          item.id
        }`,
        mode === 'share'
          ? {
              ...common,
              passwordMode,
              ...(passwordMode === 'custom'
                ? {
                    password,
                  }
                : {}),
              showOwner,
              description,
              descriptionFormat,
            }
          : common
      );
      onSaved();
      if (response.data.generatedPassword) {
        setGeneratedPassword(response.data.generatedPassword);
      } else {
        Message.success(uiText('配置已保存，原链接保持不变'));
        onClose();
      }
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('保存配置失败'));
    } finally {
      setLoading(false);
    }
  };
  const copyPassword = async () => {
    const copied = await writeClipboard(generatedPassword);
    Message[copied ? 'success' : 'error'](
      uiText(copied ? '密码已复制' : '复制失败')
    );
  };
  return (
    <Modal
      visible={visible}
      title={mode === 'share' ? uiText('配置分享') : uiText('配置直链')}
      onCancel={onClose}
      onOk={generatedPassword ? onClose : save}
      okText={generatedPassword ? uiText('完成') : uiText('保存配置')}
      cancelText={uiText('取消')}
      hideCancel={Boolean(generatedPassword)}
      confirmLoading={loading}
      maskClosable={false}
      unmountOnExit
      style={{
        width: 'min(620px, calc(100vw - 24px))',
      }}
    >
      {generatedPassword ? (
        <div className={styles['result-box']}>
          <Typography.Title
            heading={6}
            style={{
              marginTop: 0,
            }}
          >
            <IconCheckCircle
              style={{
                color: 'rgb(var(--success-6))',
                marginRight: 8,
              }}
            />
            {uiText('配置已保存，原链接保持不变')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {uiText('已生成新的访问密码，请及时保存。')}
          </Typography.Text>
          <div className={styles['result-line']}>
            <code>
              {uiText('密码：')}
              {generatedPassword}
            </code>
            <Button icon={<IconCopy />} onClick={copyPassword}>
              {uiText('复制')}
            </Button>
          </div>
        </div>
      ) : (
        <div className={styles['modal-grid']}>
          <div className={`${styles['modal-field']} ${styles.wide}`}>
            <Typography.Text type="secondary">
              {uiText('当前资源')}
            </Typography.Text>
            <Typography.Text bold>{item?.resource.name}</Typography.Text>
          </div>
          <div className={styles['modal-field']}>
            <Typography.Text>{uiText('有效期')}</Typography.Text>
            <Select
              value={expiry}
              options={expiryOptions()}
              onChange={setExpiry}
            />
          </div>
          <div className={styles['modal-field']}>
            <Typography.Text>{uiText('下载次数限制')}</Typography.Text>
            <InputNumber
              min={1}
              placeholder={uiText('不限制')}
              value={downloadLimit}
              onChange={setDownloadLimit}
            />
          </div>
          <div className={styles['modal-field']}>
            <Typography.Text>{uiText('下载流量限制')}</Typography.Text>
            <InputNumber
              min={0.01}
              precision={2}
              suffix="GB"
              placeholder={uiText('不限制')}
              value={trafficGB}
              onChange={setTrafficGB}
            />
          </div>
          {mode === 'share' && (
            <>
              <div className={`${styles['modal-field']} ${styles.wide}`}>
                <Typography.Text>{uiText('访问密码')}</Typography.Text>
                <Radio.Group
                  type="button"
                  value={passwordMode}
                  onChange={setPasswordMode}
                >
                  <Radio value="keep">{uiText('保持当前')}</Radio>
                  <Radio value="random">{uiText('重新随机')}</Radio>
                  <Radio value="custom">{uiText('自定义')}</Radio>
                  <Radio value="none">{uiText('无密码')}</Radio>
                </Radio.Group>
              </div>
              {passwordMode === 'custom' && (
                <div className={`${styles['modal-field']} ${styles.wide}`}>
                  <Typography.Text>{uiText('自定义密码')}</Typography.Text>
                  <Input.Password
                    maxLength={128}
                    value={password}
                    onChange={setPassword}
                    placeholder={uiText('请输入分享密码')}
                  />
                </div>
              )}
              <div className={`${styles['modal-field']} ${styles.wide}`}>
                <Checkbox checked={showOwner} onChange={setShowOwner}>
                  {uiText('在分享页显示我的用户名和头像')}
                </Checkbox>
              </div>
              <div className={styles['modal-field']}>
                <Typography.Text>{uiText('说明格式')}</Typography.Text>
                <Select
                  value={descriptionFormat}
                  onChange={setDescriptionFormat}
                  options={[
                    {
                      label: 'Markdown',
                      value: 'markdown',
                    },
                    {
                      label: 'HTML',
                      value: 'html',
                    },
                  ]}
                />
              </div>
              <div className={`${styles['modal-field']} ${styles.wide}`}>
                <Typography.Text>{uiText('分享页说明')}</Typography.Text>
                <Input.TextArea
                  autoSize={{
                    minRows: 3,
                    maxRows: 6,
                  }}
                  value={description}
                  onChange={setDescription}
                  placeholder={uiText('可选，填写分享内容说明')}
                />
              </div>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
