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
import { ResourceItem } from '../storage/shared';
import styles from '../storage/style/index.module.less';
import uiText from '@/utils/uiText';
import writeClipboard from '@/utils/clipboard';
type Mode = 'share' | 'direct';
interface Props {
  mode: Mode;
  resources: ResourceItem[];
  visible: boolean;
  onClose: () => void;
  allowLinkShare?: boolean;
  allowPickupShare?: boolean;
}
const expiryOptions = () => [
  { label: uiText('1 小时'), value: '3600' },
  {
    label: uiText('1 天'),
    value: '86400',
  },
  {
    label: uiText('7 天'),
    value: '604800',
  },
  {
    label: uiText('30 天'),
    value: '2592000',
  },
  {
    label: uiText('永久有效'),
    value: '0',
  },
];
export default function LinkModal({
  mode,
  resources,
  visible,
  onClose,
  allowLinkShare = true,
  allowPickupShare = true,
}: Props) {
  const resource = resources[0];
  const resourceKey = resources.map((item) => item.id).join(',');
  const [expiry, setExpiry] = useState('86400');
  const [shareType, setShareType] = useState<'link' | 'pickup'>('link');
  const [downloadLimit, setDownloadLimit] = useState<number>();
  const [trafficGB, setTrafficGB] = useState<number>();
  const [passwordMode, setPasswordMode] = useState('random');
  const [password, setPassword] = useState('');
  const [showOwner, setShowOwner] = useState(false);
  const [descriptionFormat, setDescriptionFormat] = useState('markdown');
  const [description, setDescription] = useState('');
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<{
    url: string;
    generatedPassword?: string;
    pickupCode?: string;
  }>();
  useEffect(() => {
    if (!visible) return;
    setExpiry(allowLinkShare ? '86400' : '3600');
    setShareType(allowLinkShare ? 'link' : 'pickup');
    setDownloadLimit(undefined);
    setTrafficGB(undefined);
    setPasswordMode(allowLinkShare ? 'random' : 'none');
    setPassword('');
    setShowOwner(false);
    setDescriptionFormat('markdown');
    setDescription('');
    setResult(undefined);
  }, [visible, resourceKey, mode, allowLinkShare, allowPickupShare]);
  const create = async () => {
    if (!resource) return;
    if (
      mode === 'direct' &&
      (resources.length !== 1 || resource.kind !== 'file')
    ) {
      Message.warning(uiText('直链仅支持单个文件'));
      return;
    }
    if (mode === 'share' && passwordMode === 'custom' && !password) {
      Message.warning(uiText('请输入分享密码'));
      return;
    }
    setLoading(true);
    try {
      const common = {
        ...(mode === 'share' && shareType === 'pickup'
          ? {}
          : { expiresInSeconds: Number(expiry) }),
        downloadLimit: downloadLimit ?? null,
        trafficLimitBytes:
          trafficGB == null ? null : Math.round(trafficGB * 1024 * 1024 * 1024),
      };
      const response =
        mode === 'share'
          ? await axios.post('/api/shares', {
              ...common,
              shareType,
              resourceIds: resources.map((item) => item.id),
              noPassword: passwordMode === 'none',
              ...(passwordMode === 'custom'
                ? {
                    password,
                  }
                : {}),
              showOwner,
              description,
              descriptionFormat,
            })
          : await axios.post('/api/direct-links', {
              ...common,
              resourceId: resource.id,
            });
      setResult({
        url: response.data.url
          ? new URL(response.data.url, window.location.origin).toString()
          : '',
        generatedPassword: response.data.generatedPassword,
        pickupCode: response.data.pickupCode,
      });
    } catch (error) {
      Message.error(
        error?.response?.data?.msg ||
          (mode === 'share' ? uiText('创建分享失败') : uiText('创建直链失败'))
      );
    } finally {
      setLoading(false);
    }
  };
  const copyText = async (value: string) => {
    const copied = await writeClipboard(value);
    Message[copied ? 'success' : 'error'](
      uiText(copied ? '已复制' : '复制失败')
    );
  };
  return (
    <Modal
      visible={visible}
      title={mode === 'share' ? uiText('生成分享') : uiText('生成直链')}
      onCancel={onClose}
      onOk={result ? onClose : create}
      okText={result ? uiText('完成') : uiText('生成')}
      cancelText={uiText('取消')}
      hideCancel={Boolean(result)}
      confirmLoading={loading}
      maskClosable={false}
      unmountOnExit
      style={{
        width: 'min(620px, calc(100vw - 24px))',
      }}
    >
      {result ? (
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
            {mode === 'share' ? uiText('分享已生成') : uiText('直链已生成')}
          </Typography.Title>
          <Typography.Text type="secondary">
            {result.pickupCode
              ? uiText('取件码仅在生成后提供，请及时保存。')
              : uiText('完整链接仅在生成后提供，请及时保存。')}
          </Typography.Text>
          {result.pickupCode ? (
            <>
              <div className={styles['result-line']}>
                <code>{uiText('取件码')}：{result.pickupCode}</code>
                <Button icon={<IconCopy />} onClick={() => copyText(result.pickupCode as string)}>{uiText('复制')}</Button>
              </div>
              <div className={styles['result-line']}>
                <code>{uiText('取件页面')}：{result.url}</code>
                <Button icon={<IconCopy />} onClick={() => copyText(result.url)}>{uiText('复制链接')}</Button>
              </div>
            </>
          ) : (
            <div className={styles['result-line']}>
              <code>{result.url}</code>
              <Button icon={<IconCopy />} onClick={() => copyText(result.url)}>{uiText('复制')}</Button>
            </div>
          )}
          {result.generatedPassword && (
            <div className={styles['result-line']}>
              <code>
                {uiText('密码：')}
                {result.generatedPassword}
              </code>
              <Button
                icon={<IconCopy />}
                onClick={() => copyText(result.generatedPassword)}
              >
                {uiText('复制')}
              </Button>
            </div>
          )}
        </div>
      ) : (
        <div className={styles['modal-grid']}>
          {mode === 'share' && allowLinkShare && allowPickupShare && (
            <div className={`${styles['modal-field']} ${styles.wide}`}>
              <Typography.Text>{uiText('分享方式')}</Typography.Text>
              <Radio.Group type="button" value={shareType} onChange={(value) => {
                setShareType(value);
                if (value === 'link') {
                  setExpiry('86400');
                  setPasswordMode('random');
                } else {
                  setPasswordMode('none');
                }
              }}>
                <Radio value="link">{uiText('链接')}</Radio>
                <Radio value="pickup">{uiText('取件码')}</Radio>
              </Radio.Group>
            </div>
          )}
          {resources.length === 1 && (
            <div className={`${styles['modal-field']} ${styles.wide}`}>
              <Typography.Text type="secondary">
                {uiText('当前资源')}
              </Typography.Text>
              <Typography.Text bold>{resource?.name}</Typography.Text>
            </div>
          )}
          {shareType !== 'pickup' && <div className={styles['modal-field']}>
            <Typography.Text>{uiText('有效期')}</Typography.Text>
            <Select
              value={expiry}
              options={expiryOptions()}
              onChange={setExpiry}
            />
          </div>}
          <div className={styles['modal-field']}>
            <Typography.Text>{uiText('下载次数限制')}</Typography.Text>
            <InputNumber
              min={1}
              precision={0}
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
              <div className={styles['modal-field']}>
                <Typography.Text>{uiText('访问密码')}</Typography.Text>
                <Radio.Group
                  type="button"
                  value={passwordMode}
                  onChange={setPasswordMode}
                >
                  <Radio value="random">{uiText('随机')}</Radio>
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
