import { fetchAdminSystemConfig, updateAdminSystemConfig, testAdminUpload, uploadSiteIcon, deleteSiteIcon, fetchCustomHomepage, saveCustomHomepage, deleteCustomHomepage } from '@/api/endpoints';
import { apiErrorMessage } from '@/api/client';
import { useHistory } from 'react-router-dom';
import React, { useContext, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  InputNumber,
  Message,
  Modal,
  Radio,
  Spin,
  Switch,
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
  const history = useHistory();
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
  // 部署是否配置了加密密钥（XPH_ENCRYPTION_KEYS）：未配置时不允许开启加密。
  const [encryptionConfigured, setEncryptionConfigured] = useState(false);
  /** 服务端配置 → 表单值：首次加载与保存后回填共用同一份换算，避免两处默认值漂移。 */
  const toFormValues = (data: Record<string, unknown>) => ({
    ...data,
    pickupLifetimeHours:
      data.pickupMaxLifetimeSeconds == null
        ? undefined
        : (data.pickupMaxLifetimeSeconds as number) / 3600,
    pickupAllowPermanent: (data.pickupAllowPermanent as boolean | undefined) ?? true,
    invitationValidDays: (data.invitationValidDays as number | undefined) ?? 90,
    crossUserDedupe: (data.crossUserDedupe as boolean | undefined) ?? true,
    redirectFallback: (data.redirectFallback as boolean | undefined) ?? true,
    uploadMaxFileGB:
      ((data.uploadMaxFileBytes as number) || 100 * 1024 ** 3) / 1024 ** 3,
    uploadChunkSizeMB:
      ((data.uploadChunkSizeBytes as number) || 8 * 1024 * 1024) / 1024 / 1024,
    storageChunkSizeMB: ((data.storageChunkSizeBytes as number) || 0) / 1024 / 1024,
  });
  useEffect(() => {
    fetchAdminSystemConfig()
      .then((res) => {
        form.setFieldsValue(toFormValues(res.data));
        setIconUrl(res.data.siteIconUrl || '');
        setEncryptionConfigured(Boolean(res.data.encryptionConfigured));
        setCustomHomepageConfigured(Boolean(res.data.customHomepageConfigured));
        setCustomHomepageEnabled(Boolean(res.data.customHomepageConfigured));
        if (res.data.customHomepageConfigured) {
          fetchCustomHomepage().then((homepageRes) => {
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
    const {
      uploadChunkSizeMB,
      pickupLifetimeHours,
      storageChunkSizeMB,
      uploadMaxFileGB,
      ...payload
    } = values;
    const res = await updateAdminSystemConfig({
      ...payload,
      pickupMaxLifetimeSeconds:
        pickupLifetimeHours == null || pickupLifetimeHours === ''
          ? null
          : pickupLifetimeHours * 3600,
      uploadChunkSizeBytes: uploadChunkSizeMB * 1024 * 1024,
      storageChunkSizeBytes: (storageChunkSizeMB || 0) * 1024 * 1024,
      uploadMaxFileBytes: Math.round((uploadMaxFileGB ?? 100) * 1024 ** 3),
    });
    form.setFieldsValue(toFormValues(res.data));
    setIconUrl(res.data.siteIconUrl || '');
    setEncryptionConfigured(Boolean(res.data.encryptionConfigured));
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
      Message.error(apiErrorMessage(error, uiText('保存失败')));
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
      const res = await uploadSiteIcon(body);
      setIconUrl(res.data.siteIconUrl || '');
      setSiteConfig?.({
        siteIconUrl: res.data.siteIconUrl || '',
      });
      Message.success(uiText('站点图标已上传并实时生效'));
    } catch (error) {
      // 上传前会先保存整页表单：校验失败时给出真实原因，而不是"上传失败"。
      if ((error as { errorFields?: unknown })?.errorFields) {
        Message.error(uiText('请先完善表单中的存储配置'));
        return;
      }
      Message.error(apiErrorMessage(error, uiText('图标上传失败')));
    } finally {
      setIconUploading(false);
      if (iconInputRef.current) iconInputRef.current.value = '';
    }
  };
  const removeIcon = async () => {
    try {
      setIconUploading(true);
      const res = await deleteSiteIcon();
      setIconUrl('');
      setSiteConfig?.({
        siteIconUrl: res.data.siteIconUrl || '',
      });
      Message.success(uiText('已恢复默认站点图标'));
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('恢复默认图标失败')));
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
      const res = await saveCustomHomepage({
        html: customHomepageHTML,
      });
      setCustomHomepageConfigured(Boolean(res.data.customHomepageConfigured));
      setCustomHomepageEnabled(true);
      Message.success(uiText('自定义首页已保存并实时生效'));
    } catch (error) {
      if ((error as { errorFields?: unknown })?.errorFields) {
        Message.error(uiText('请先完善表单中的存储配置'));
        return;
      }
      Message.error(apiErrorMessage(error, uiText('首页保存失败')));
    } finally {
      setHomepageUploading(false);
    }
  };
  const removeHomepage = async () => {
    try {
      setHomepageUploading(true);
      await deleteCustomHomepage();
      setCustomHomepageConfigured(false);
      setCustomHomepageEnabled(false);
      setCustomHomepageHTML('');
      Message.success(uiText('已恢复默认首页行为'));
    } catch (error) {
      Message.error(apiErrorMessage(error, uiText('恢复默认首页失败')));
    } finally {
      setHomepageUploading(false);
    }
  };
  const testUploadChunk = async () => {
    try {
      const values = await form.validate(['uploadChunkSizeMB']);
      const sizeBytes = values.uploadChunkSizeMB * 1024 * 1024;
      setChunkTesting(true);
      await testAdminUpload(sizeBytes, new Blob([new Uint8Array(sizeBytes)]),
        { headers: { 'Content-Type': 'application/octet-stream' } }
      );
      Message.success(uiText('分片大小验证通过'));
    } catch (error) {
      // 表单校验失败抛出的是 errorFields 对象而非接口错误：给出"请先填写"，
      // 不要复用泛化的"验证失败"。
      if ((error as { errorFields?: unknown })?.errorFields) {
        Message.error(uiText('请先填写分片大小'));
        return;
      }
      Message.error(apiErrorMessage(error, uiText('分片大小验证失败')));
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
                    if (checked) {
                      setCustomHomepageEnabled(true);
                      return;
                    }
                    if (!customHomepageConfigured) {
                      setCustomHomepageEnabled(false);
                      return;
                    }
                    // 取消勾选会删除已保存的自定义首页，先确认再执行。
                    Modal.confirm({
                      title: uiText('移除自定义首页'),
                      content: uiText('将删除已保存的自定义首页内容，此操作不可撤销。'),
                      okText: uiText('确认移除'),
                      cancelText: uiText('取消'),
                      onOk: () => removeHomepage(),
                    });
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
                    label={uiText('单文件上限（GB）')}
                    field="uploadMaxFileGB"
                    rules={[{ required: true, type: 'number', min: 1 }]}
                  >
                    <InputNumber min={1} step={1} precision={0} suffix="GB" />
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
                <FormItem field="crossUserDedupe" triggerPropName="checked">
                  <Checkbox>
                    {uiText('允许跨用户秒传复用（关闭后仅复用自己已有的文件，更保护隐私）')}
                  </Checkbox>
                </FormItem>
                <Text type="secondary">
                  {uiText(
                    '开启时按内容哈希全平台去重：任何用户知道文件的 SHA-256 与大小即可秒传他人的私有文件（省存储但可能越权读取）；关闭后只与自己已有的对象去重。'
                  )}
                </Text>
              </div>
              <div className={styles['config-section']}>
                <div className={styles['config-title']}>
                  {uiText('登录动态令牌')}
                </div>
                <FormItem field="loginTOTPEnabled" triggerPropName="checked">
                  <Checkbox>{uiText('启用登录动态令牌')}</Checkbox>
                </FormItem>
                <Form.Item shouldUpdate noStyle>
                  {(values) => values.loginTOTPEnabled ? (
                    <>
                      <Button onClick={() => history.push('/admin/access?tab=permissions')}>
                        {uiText('前往配置用户组权限')}
                      </Button>
                      <div style={{ marginTop: 8, minWidth: 0 }}>
                        <Text type="secondary" ellipsis={{ showTooltip: true }} style={{ display: 'block' }}>
                          {uiText('当前允许使用的用户组：')}{(values.loginTOTPAllowedGroups || []).join('、') || uiText('无')}
                        </Text>
                        <Text type="secondary" ellipsis={{ showTooltip: true }} style={{ display: 'block' }}>
                          {uiText('强制使用的用户组：')}{(values.loginTOTPRequiredGroups || []).join('、') || uiText('无')}
                        </Text>
                      </div>
                    </>
                  ) : null}
                </Form.Item>
              </div>
              <div className={styles['config-section']}>
                <div className={styles['config-title']}>{uiText('回收站')}</div>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '到期内容将自动永久删除，默认保留 30 天。缩短保留期后，超出期限的内容会在下一次清理时立即彻底删除。'
                  )}
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
                    <FormItem
                      label={uiText('邀请码有效期（天，0 = 永久）')}
                      field="invitationValidDays"
                      rules={[{ type: 'number', min: 0, max: 3650 }]}
                    >
                      <InputNumber
                        min={0}
                        max={3650}
                        precision={0}
                        placeholder={uiText('0 表示永久')}
                      />
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
                    <FormItem label={uiText('取件码默认有效期')} field="pickupLifetimeHours" rules={[{ type: 'number', min: 1 }]}>
                      <InputNumber min={1} step={1} precision={0} suffix={uiText('小时')} placeholder={uiText('永久有效')} />
                    </FormItem>
                    <FormItem field="pickupAllowPermanent" triggerPropName="checked">
                      <Checkbox>
                        {uiText('允许永久有效的取件码（码空间有限，可在分享页「清理失效取件码」腾位置）')}
                      </Checkbox>
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
                    '所有交付（分享页、文件页、管理端审核）均由浏览器接收：取数、解密、合并与打包在前端完成，服务端只负责鉴权、发地址与计数。'
                  )}
                </Text>
                <FormItem
                  label={uiText('分享取数方式')}
                  field="shareRetrievalMode"
                  rules={[
                    {
                      required: true,
                    },
                  ]}
                >
                  <Radio.Group type="button">
                    <Radio value="redirect">{uiText('302 优先')}</Radio>
                    <Radio value="proxy">{uiText('中转优先')}</Radio>
                  </Radio.Group>
                </FormItem>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '302 优先（默认）：浏览器逐片直连第三方存储逐片取数并自行解密合并，流量不过服务器（需存储后端启用直链）。中转优先：一律经由本机中转，由前端解密或服务器兜底解密。'
                  )}
                </Text>
                <FormItem
                  label={uiText('302 不可用时自动降级中转')}
                  field="redirectFallback"
                  triggerPropName="checked"
                >
                  <Switch />
                </FormItem>
                <Text type="secondary" className={styles['config-description']}>
                  {uiText(
                    '开启后：302 不可用（存储后端未启用直链、或浏览器无法完成解密）时自动改走本机中转；关闭后直接提示下载失败（不展示内部原因）。'
                  )}
                </Text>
              </div>
              <div
                className={`${styles['config-section']} ${styles['config-section-wide']}`}
              >
                <div className={styles['config-title']}>
                  {uiText('存储加密')}
                </div>
                <FormItem
                  label={uiText('新文件加密存储')}
                  field="encryptNewFiles"
                  triggerPropName="checked"
                  disabled={!encryptionConfigured}
                  extra={
                    encryptionConfigured
                      ? undefined
                      : uiText('当前部署未配置加密密钥')
                  }
                >
                  <Switch disabled={!encryptionConfigured} />
                </FormItem>
                <Text type="secondary" className={styles['config-description']}>
                  {encryptionConfigured
                    ? uiText(
                        '开启后新上传的文件使用 AES-256-GCM 分块加密存储（每个文件独立随机密钥，密钥经部署密钥包裹后保存在数据库）；关闭时新文件保持明文存储，历史文件不受影响。'
                      )
                    : uiText(
                        '当前部署未配置加密密钥（XPH_ENCRYPTION_KEYS），无法开启新文件加密。'
                      )}
                </Text>
                <FormItem
                  label={uiText('新上传分片大小（MiB）')}
                  field="storageChunkSizeMB"
                  rules={[{ required: true }]}
                  extra={uiText(
                    '分片存储为强制项（不可关闭）：必须是 4MiB 的整数倍（4~1024），默认 64MiB。分片与加密兼容（分片边界与加密块对齐），已有对象可在「存储管理」用分片任务重写。'
                  )}
                >
                  <InputNumber min={4} max={1024} step={4} style={{ width: 200 }} />
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
