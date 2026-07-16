import React, { useContext, useEffect, useRef, useState } from 'react';
import axios from 'axios';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Message,
  Radio,
  Spin,
  Typography,
} from '@arco-design/web-react';
import { IconDelete, IconSave, IconUpload } from '@arco-design/web-react/icon';
import logoUrl from '@/assets/logo.svg';
import { GlobalContext } from '@/context';
import { AdminPageHeader } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
const { Text } = Typography;
const FormItem = Form.Item;
function SystemConfig() {
  const [form] = Form.useForm();
  const { setSiteConfig } = useContext(GlobalContext);
  const iconInputRef = useRef<HTMLInputElement>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [iconUploading, setIconUploading] = useState(false);
  const [iconUrl, setIconUrl] = useState('');
  useEffect(() => {
    axios
      .get('/api/admin/system-config')
      .then((res) => {
        form.setFieldsValue(res.data);
        setIconUrl(res.data.siteIconUrl || '');
      })
      .catch(() => Message.error(uiText('系统配置加载失败')))
      .finally(() => setLoading(false));
  }, [form]);
  const persistConfig = async (showSuccess: boolean) => {
    const values = await form.validate();
    if (
      (!values.invitationCodeIncludeLetters &&
        !values.invitationCodeIncludeNumbers) ||
      (!values.shareCodeIncludeLetters && !values.shareCodeIncludeNumbers)
    ) {
      Message.error(uiText('邀请码和分享码都必须至少包含字母或数字'));
      throw new Error('invalid random code charset');
    }
    const res = await axios.put('/api/admin/system-config', values);
    form.setFieldsValue(res.data);
    setIconUrl(res.data.siteIconUrl || '');
    setSiteConfig?.({
      siteName: res.data.siteName,
      siteIconUrl: res.data.siteIconUrl || '',
    });
    if (showSuccess) Message.success(uiText('系统配置已保存并实时生效'));
    return res.data;
  };
  const save = async () => {
    try {
      setSaving(true);
      await persistConfig(true);
    } catch (error) {
      if (error?.response)
        Message.error(error.response.data?.msg || uiText('保存失败'));
    } finally {
      setSaving(false);
    }
  };
  const uploadIcon = async (file: File) => {
    try {
      setIconUploading(true);
      // 先保存表单，保证图标写入页面上当前设置的存储根目录。
      await persistConfig(false);
      const body = new FormData();
      body.append('icon', file);
      const res = await axios.post('/api/admin/site-icon', body);
      setIconUrl(res.data.siteIconUrl || '');
      setSiteConfig?.({
        siteIconUrl: res.data.siteIconUrl || '',
      });
      Message.success(uiText('站点图标已上传并实时生效'));
    } catch (error) {
      if (error?.response) {
        Message.error(error.response.data?.msg || uiText('图标上传失败'));
      }
    } finally {
      setIconUploading(false);
      if (iconInputRef.current) iconInputRef.current.value = '';
    }
  };
  const removeIcon = async () => {
    try {
      setIconUploading(true);
      const res = await axios.delete('/api/admin/site-icon');
      setIconUrl('');
      setSiteConfig?.({
        siteIconUrl: res.data.siteIconUrl || '',
      });
      Message.success(uiText('已恢复默认站点图标'));
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('恢复默认图标失败'));
    } finally {
      setIconUploading(false);
    }
  };
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('系统配置')}
        description={uiText('调整站点基础信息、存储位置和全局下载行为。')}
      />
      <Spin
        loading={loading}
        style={{
          width: '100%',
        }}
      >
        <Card className={styles['section-card']}>
          <Alert
            type="warning"
            showIcon
            content={uiText(
              '修改文件存储路径后，新上传和后续下载会立即使用新目录，请确认运行账户拥有读写权限。'
            )}
            style={{
              marginBottom: 20,
            }}
          />
          <Form form={form} layout="vertical">
            <div className={styles['config-grid']}>
              <div className={styles['config-section']}>
                <div className={styles['config-title']}>
                  {uiText('站点信息')}
                </div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText('用于页面标题和产品标识。')}
                </Text>
                <FormItem
                  label={uiText('站点名称')}
                  field="siteName"
                  rules={[
                    {
                      required: true,
                      message: uiText('请输入站点名称'),
                    },
                  ]}
                >
                  <Input maxLength={80} placeholder="XiaoyuPostHub" />
                </FormItem>
                <FormItem label={uiText('站点图标')}>
                  <div className={styles['site-icon-setting']}>
                    <div className={styles['site-icon-preview']}>
                      <img
                        src={iconUrl || logoUrl}
                        alt={uiText('当前站点图标')}
                      />
                    </div>
                    <div className={styles['site-icon-actions']}>
                      <input
                        ref={iconInputRef}
                        type="file"
                        accept="image/svg+xml,image/png,image/jpeg,image/webp"
                        hidden
                        onChange={(event) => {
                          const file = event.target.files?.[0];
                          if (file) uploadIcon(file);
                        }}
                      />
                      <Button
                        icon={<IconUpload />}
                        loading={iconUploading}
                        onClick={() => iconInputRef.current?.click()}
                      >
                        {uiText('上传或替换')}
                      </Button>
                      {iconUrl && (
                        <Button
                          status="danger"
                          icon={<IconDelete />}
                          disabled={iconUploading}
                          onClick={removeIcon}
                        >
                          {uiText('恢复默认')}
                        </Button>
                      )}
                    </div>
                  </div>
                  <Text type="secondary" className={styles['site-icon-hint']}>
                    {uiText('支持 SVG、PNG、JPEG、WebP，最大 2MB。')}
                  </Text>
                </FormItem>
              </div>
              <div className={styles['config-section']}>
                <div className={styles['config-title']}>
                  {uiText('文件存储')}
                </div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText('必须填写服务器上的绝对路径。')}
                </Text>
                <FormItem
                  label={uiText('存储路径')}
                  field="storagePath"
                  rules={[
                    {
                      required: true,
                      match: /^\//,
                      message: uiText('请输入绝对路径'),
                    },
                  ]}
                >
                  <Input placeholder="/data/uploads" />
                </FormItem>
              </div>
              <div
                className={`${styles['config-section']} ${styles['config-section-wide']}`}
              >
                <div className={styles['config-title']}>{uiText('随机码')}</div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '保存后仅影响新生成的邀请码和随机分享码，已有内容保持不变。'
                  )}
                </Text>
                <div className={styles['random-code-grid']}>
                  <div className={styles['random-code-group']}>
                    <Typography.Title heading={6}>
                      {uiText('邀请码')}
                    </Typography.Title>
                    <FormItem
                      label={uiText('位数')}
                      field="invitationCodeLength"
                      rules={[
                        {
                          required: true,
                        },
                      ]}
                    >
                      <InputNumber min={4} max={64} precision={0} />
                    </FormItem>
                    <div className={styles['random-code-options']}>
                      <FormItem
                        field="invitationCodeCaseSensitive"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('区分大小写')}</Checkbox>
                      </FormItem>
                      <FormItem
                        field="invitationCodeIncludeLetters"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('包含字母')}</Checkbox>
                      </FormItem>
                      <FormItem
                        field="invitationCodeIncludeNumbers"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('包含数字')}</Checkbox>
                      </FormItem>
                    </div>
                  </div>
                  <div className={styles['random-code-group']}>
                    <Typography.Title heading={6}>
                      {uiText('分享码')}
                    </Typography.Title>
                    <FormItem
                      label={uiText('位数')}
                      field="shareCodeLength"
                      rules={[
                        {
                          required: true,
                        },
                      ]}
                    >
                      <InputNumber min={4} max={64} precision={0} />
                    </FormItem>
                    <div className={styles['random-code-options']}>
                      <FormItem
                        field="shareCodeCaseSensitive"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('区分大小写')}</Checkbox>
                      </FormItem>
                      <FormItem
                        field="shareCodeIncludeLetters"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('包含字母')}</Checkbox>
                      </FormItem>
                      <FormItem
                        field="shareCodeIncludeNumbers"
                        triggerPropName="checked"
                      >
                        <Checkbox>{uiText('包含数字')}</Checkbox>
                      </FormItem>
                    </div>
                  </div>
                </div>
              </div>
              <div
                className={`${styles['config-section']} ${styles['config-section-wide']}`}
              >
                <div className={styles['config-title']}>
                  {uiText('内容审核')}
                </div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '仅影响保存配置后新上传的文件和新提交的自定义分享说明。'
                  )}
                </Text>
                <FormItem
                  field="uploadRequiresReview"
                  triggerPropName="checked"
                >
                  <Checkbox>{uiText('上传文件需要先审核')}</Checkbox>
                </FormItem>
                <FormItem
                  field="customShareRequiresReview"
                  triggerPropName="checked"
                >
                  <Checkbox>{uiText('自定义分享说明需要先审核')}</Checkbox>
                </FormItem>
              </div>
              <div
                className={`${styles['config-section']} ${styles['config-section-wide']}`}
              >
                <div className={styles['config-title']}>
                  {uiText('下载策略')}
                </div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '前端打包适合小型目录，后端打包对大目录更稳定；临时链接可交给浏览器直接下载。'
                  )}
                </Text>
                <FormItem
                  label={uiText('文件夹打包位置')}
                  field="folderPackMode"
                  rules={[
                    {
                      required: true,
                    },
                  ]}
                >
                  <Radio.Group type="button">
                    <Radio value="backend">
                      {uiText('后端校验并打包 ZIP')}
                    </Radio>
                    <Radio value="frontend">{uiText('前端逐文件打包')}</Radio>
                  </Radio.Group>
                </FormItem>
                <FormItem
                  label={uiText('分享页文件交付')}
                  field="shareDeliveryMode"
                  rules={[
                    {
                      required: true,
                    },
                  ]}
                >
                  <Radio.Group type="button">
                    <Radio value="blob">{uiText('前端读取 Blob 流')}</Radio>
                    <Radio value="temporary_link">
                      {uiText('一次性临时链接')}
                    </Radio>
                  </Radio.Group>
                </FormItem>
              </div>
            </div>
            <div className={styles['form-actions']}>
              <Button
                type="primary"
                icon={<IconSave />}
                loading={saving}
                onClick={save}
              >
                {uiText('保存配置')}
              </Button>
            </div>
          </Form>
        </Card>
      </Spin>
    </div>
  );
}
export default SystemConfig;
