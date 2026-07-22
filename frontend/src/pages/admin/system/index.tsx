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
  const homepageInputRef = useRef<HTMLInputElement>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [iconUploading, setIconUploading] = useState(false);
  const [homepageUploading, setHomepageUploading] = useState(false);
  const [chunkTesting, setChunkTesting] = useState(false);
  const [iconUrl, setIconUrl] = useState('');
  const [customHomepageConfigured, setCustomHomepageConfigured] =
    useState(false);
  const [customHomepageEnabled, setCustomHomepageEnabled] = useState(false);
  const [customHomepageHTML, setCustomHomepageHTML] = useState('');
  useEffect(() => {
    axios
      .get('/api/admin/system-config')
      .then((res) => {
        form.setFieldsValue({
          ...res.data,
          pickupLifetimeHours:
            res.data.pickupMaxLifetimeSeconds == null
              ? 0
              : res.data.pickupMaxLifetimeSeconds / 3600,
          uploadChunkSizeMB:
            (res.data.uploadChunkSizeBytes || 8 * 1024 * 1024) / 1024 / 1024,
        });
        setIconUrl(res.data.siteIconUrl || '');
        setCustomHomepageConfigured(Boolean(res.data.customHomepageConfigured));
        setCustomHomepageEnabled(Boolean(res.data.customHomepageConfigured));
        if (res.data.customHomepageConfigured) {
          axios.get('/api/admin/homepage').then((homepageRes) => {
            setCustomHomepageHTML(homepageRes.data.html || '');
          }).catch(() => Message.error(uiText('自定义首页内容加载失败')));
        }
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
      || (!values.pickupCodeIncludeLetters && !values.pickupCodeIncludeNumbers)
    ) {
      Message.error(uiText('邀请码和分享码都必须至少包含字母或数字'));
      throw new Error('invalid random code charset');
    }
    const { uploadChunkSizeMB, pickupLifetimeHours, ...payload } = values;
    const res = await axios.put('/api/admin/system-config', {
      ...payload,
      pickupMaxLifetimeSeconds:
        pickupLifetimeHours > 0 ? Math.round(pickupLifetimeHours * 3600) : null,
      uploadChunkSizeBytes: uploadChunkSizeMB * 1024 * 1024,
    });
    form.setFieldsValue({
      ...res.data,
      pickupLifetimeHours:
        res.data.pickupMaxLifetimeSeconds == null
          ? 0
          : res.data.pickupMaxLifetimeSeconds / 3600,
      uploadChunkSizeMB: res.data.uploadChunkSizeBytes / 1024 / 1024,
    });
    setIconUrl(res.data.siteIconUrl || '');
    setSiteConfig?.({
      siteName: res.data.siteName,
      siteIconUrl: res.data.siteIconUrl || '',
    });
    window.dispatchEvent(
      new CustomEvent('xph-upload-config-updated', {
        detail: {
          taskChunkConcurrency: res.data.uploadTaskChunkConcurrency,
          userTaskConcurrency: res.data.uploadUserTaskConcurrency,
        },
      })
    );
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
  const loadHomepageFile = async (file: File) => {
    try {
      setCustomHomepageHTML(await file.text());
      setCustomHomepageEnabled(true);
      Message.success(uiText('HTML 文件已读取，请确认内容后保存'));
    } catch (error) {
      Message.error(uiText('HTML 文件读取失败'));
    } finally {
      if (homepageInputRef.current) homepageInputRef.current.value = '';
    }
  };
  const saveHomepageHTML = async () => {
    if (!customHomepageHTML.trim()) {
      Message.warning(uiText('请输入自定义首页 HTML'));
      return;
    }
    try {
      setHomepageUploading(true);
      await persistConfig(false);
      const res = await axios.post('/api/admin/homepage', {
        html: customHomepageHTML,
      });
      setCustomHomepageConfigured(Boolean(res.data.customHomepageConfigured));
      setCustomHomepageEnabled(true);
      Message.success(uiText('自定义首页已保存并实时生效'));
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('首页保存失败'));
    } finally {
      setHomepageUploading(false);
    }
  };
  const removeHomepage = async () => {
    try {
      setHomepageUploading(true);
      await axios.delete('/api/admin/homepage');
      setCustomHomepageConfigured(false);
      setCustomHomepageEnabled(false);
      setCustomHomepageHTML('');
      Message.success(uiText('已恢复默认首页行为'));
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('恢复默认首页失败'));
    } finally {
      setHomepageUploading(false);
    }
  };
  const testUploadChunk = async () => {
    try {
      const values = await form.validate(['uploadChunkSizeMB']);
      const sizeBytes = values.uploadChunkSizeMB * 1024 * 1024;
      setChunkTesting(true);
      await axios.post(
        `/api/admin/system-config/upload-test?sizeBytes=${sizeBytes}`,
        new Blob([new Uint8Array(sizeBytes)]),
        { headers: { 'Content-Type': 'application/octet-stream' } }
      );
      Message.success(uiText('分片大小验证通过'));
    } catch (error) {
      if (error?.response) {
        Message.error(error.response.data?.msg || uiText('分片大小验证失败'));
      }
    } finally {
      setChunkTesting(false);
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
                    {uiText('支持 SVG、PNG、JPEG 和 WebP。')}
                  </Text>
                </FormItem>
                <FormItem label={uiText('自定义首页')}>
                  <Checkbox checked={customHomepageEnabled} disabled={homepageUploading} onChange={(checked) => {
                    if (checked) setCustomHomepageEnabled(true);
                    else if (customHomepageConfigured) removeHomepage();
                    else setCustomHomepageEnabled(false);
                  }}>{uiText('启用自定义首页')}</Checkbox>
                  {customHomepageEnabled && <>
                    <Input.TextArea
                      value={customHomepageHTML}
                      onChange={setCustomHomepageHTML}
                      autoSize={{ minRows: 8, maxRows: 18 }}
                      placeholder={uiText('在此粘贴完整的 HTML 内容')}
                      style={{ marginTop: 12, fontFamily: 'monospace' }}
                    />
                    <div className={styles['site-icon-actions']} style={{ marginTop: 12 }}>
                    <input
                      ref={homepageInputRef}
                      type="file"
                      accept="text/html,.html,.htm"
                      hidden
                      onChange={(event) => {
                        const file = event.target.files?.[0];
                        if (file) loadHomepageFile(file);
                      }}
                    />
                    <Button
                      icon={<IconUpload />}
                      onClick={() => homepageInputRef.current?.click()}
                    >
                      {uiText('从 HTML 文件填充')}
                    </Button>
                    <Button
                      type="primary"
                      icon={<IconSave />}
                      loading={homepageUploading}
                      onClick={saveHomepageHTML}
                    >
                      {uiText('保存填写内容')}
                    </Button>
                    {customHomepageConfigured && (
                      <Button
                        status="danger"
                        icon={<IconDelete />}
                        disabled={homepageUploading}
                        onClick={removeHomepage}
                      >
                        {uiText('恢复默认')}
                      </Button>
                    )}
                    </div>
                  </>}
                  <Text type="secondary" className={styles['site-icon-hint']}>
                    {customHomepageConfigured
                      ? uiText('当前已启用自定义首页。')
                      : uiText('未配置时访问首页将跳转到登录页面。')}
                    {uiText('支持单个 HTML 文件，可包含内嵌 CSS 和 JavaScript。')}
                    {uiText('建议为登录页 /login、取件码 /m 等设计对应入口！')}
                  </Text>
                </FormItem>
                <FormItem field="loginTOTPEnabled" triggerPropName="checked">
                  <Checkbox>{uiText('登录动态令牌')}</Checkbox>
                </FormItem>
                <Form.Item shouldUpdate noStyle>
                  {(values) => values.loginTOTPEnabled ? (
                    <Button onClick={() => { window.location.href = '/admin/access?tab=permissions'; }}>
                      {uiText('按用户组配置')}
                    </Button>
                  ) : null}
                </Form.Item>
              </div>
              <div className={styles['config-section']}>
                <div className={styles['config-section-header']}>
                  <div>
                    <div className={styles['config-title']}>
                      {uiText('分片上传')}
                    </div>
                    <Text
                      type="secondary"
                      className={styles['config-description']}
                    >
                      {uiText('分片应小于反向代理允许的请求体大小。默认 8M。')}
                    </Text>
                  </div>
                  <Button loading={chunkTesting} onClick={testUploadChunk}>
                    {uiText('测试验证')}
                  </Button>
                </div>
                <div className={styles['upload-config-grid']}>
                  <FormItem
                    label={uiText('分片大小')}
                    field="uploadChunkSizeMB"
                    rules={[{ required: true }]}
                  >
                    <InputNumber
                      min={1}
                      max={64}
                      step={1}
                      precision={0}
                      mode="button"
                      suffix="M"
                    />
                  </FormItem>
                  <FormItem
                    label={uiText('单任务并发数')}
                    field="uploadTaskChunkConcurrency"
                    rules={[{ required: true }]}
                  >
                    <InputNumber
                      min={1}
                      max={8}
                      step={1}
                      precision={0}
                      mode="button"
                    />
                  </FormItem>
                  <FormItem
                    label={uiText('单用户任务并发数')}
                    field="uploadUserTaskConcurrency"
                    rules={[{ required: true }]}
                  >
                    <InputNumber
                      min={1}
                      max={8}
                      step={1}
                      precision={0}
                      mode="button"
                    />
                  </FormItem>
                </div>
                <Text type="secondary">
                  {uiText(
                    '单任务并发数控制一个文件同时上传的分片数，单用户任务并发数控制同时上传的文件数。'
                  )}
                </Text>
              </div>
              <div className={styles['config-section']}>
                <div className={styles['config-title']}>{uiText('回收站')}</div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText('到期内容将自动永久删除，默认保留 30 天。')}
                </Text>
                <FormItem
                  label={uiText('回收期限')}
                  field="trashRetentionDays"
                  rules={[{ required: true }]}
                >
                  <InputNumber
                    min={1}
                    max={3650}
                    step={1}
                    precision={0}
                    mode="button"
                    suffix={uiText('天')}
                  />
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
                    <Typography.Title heading={6}>{uiText('取件码')}</Typography.Title>
                    <FormItem label={uiText('位数')} field="pickupCodeLength" rules={[{ required: true }]}>
                      <InputNumber min={1} max={64} precision={0} />
                    </FormItem>
                    <div className={styles['random-code-options']}>
                      <FormItem field="pickupCodeCaseSensitive" triggerPropName="checked"><Checkbox>{uiText('区分大小写')}</Checkbox></FormItem>
                      <FormItem field="pickupCodeIncludeLetters" triggerPropName="checked"><Checkbox>{uiText('包含字母')}</Checkbox></FormItem>
                      <FormItem field="pickupCodeIncludeNumbers" triggerPropName="checked"><Checkbox>{uiText('包含数字')}</Checkbox></FormItem>
                    </div>
                    <FormItem label={uiText('取件码有效期')} field="pickupLifetimeHours" rules={[{ required: true }]}>
                      <InputNumber min={0} precision={2} suffix={uiText('小时')} />
                      <Text type="secondary">{uiText('所有取件码统一使用该有效期；填写 0 表示永久有效')}</Text>
                    </FormItem>
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
