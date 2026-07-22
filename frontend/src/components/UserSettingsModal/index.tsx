import React, { useCallback, useEffect, useState } from 'react';
import axios from 'axios';
import { Button, Input, Message, Modal, Space, Tag, Typography } from '@arco-design/web-react';
import { IconArrowLeft, IconCopy, IconSafe } from '@arco-design/web-react/icon';
import uiText from '@/utils/uiText';

interface Status { enabled: boolean; allowed: boolean; configured: boolean; required: boolean; }
interface Setup { secret: string; url: string; qrCode: string; }

export default function UserSettingsModal() {
  const [visible, setVisible] = useState(false);
  const [status, setStatus] = useState<Status>();
  const [setup, setSetup] = useState<Setup>();
  const [code, setCode] = useState('');
  const [loading, setLoading] = useState(false);
  const load = useCallback(() => axios.get('/api/user/totp').then((res) => setStatus(res.data)), []);
  useEffect(() => {
    const open = () => { setVisible(true); setSetup(undefined); setCode(''); load().catch(() => Message.error(uiText('安全配置加载失败'))); };
    window.addEventListener('xph-open-user-settings', open);
    return () => window.removeEventListener('xph-open-user-settings', open);
  }, [load]);
  const begin = async () => {
    try { setLoading(true); const res = await axios.post('/api/user/totp/begin'); setSetup(res.data.setup); }
    catch (error) { Message.error(error?.response?.data?.msg || uiText('生成动态令牌失败')); }
    finally { setLoading(false); }
  };
  const confirm = async () => {
    if (!setup || code.length !== 6) return Message.warning(uiText('请输入 6 位动态令牌'));
    try {
      setLoading(true);
      await axios.post('/api/user/totp/confirm', { secret: setup.secret, code });
      Message.success(uiText('动态令牌配置成功'));
      setSetup(undefined); await load(); window.location.reload();
    } catch (error) { Message.error(error?.response?.data?.msg || uiText('动态令牌校验失败')); }
    finally { setLoading(false); }
  };
  useEffect(() => {
    if (setup && code.length === 6 && !loading) confirm();
    // Only a code edit should trigger an automatic verification attempt.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code]);
  return <Modal title={setup ? uiText('配置动态令牌') : uiText('用户配置')} visible={visible}
    footer={null} onCancel={() => setVisible(false)} unmountOnExit>
    {setup ? <div>
      <Button type="text" icon={<IconArrowLeft />} onClick={() => { setSetup(undefined); setCode(''); }}>{uiText('返回安全配置')}</Button>
      <Typography.Paragraph>{uiText('请使用验证器应用扫描二维码，或手动复制下方链接。完成后输入应用显示的 6 位动态令牌以确认绑定。')}</Typography.Paragraph>
      <div style={{ textAlign: 'center', margin: '20px 0' }}><img src={setup.qrCode} alt={uiText('动态令牌二维码')} width={220} height={220} /></div>
      <Input value={setup.url} readOnly suffix={<Button type="text" icon={<IconCopy />} onClick={() => navigator.clipboard.writeText(setup.url).then(() => Message.success(uiText('已复制')))} />} />
      <Input style={{ marginTop: 16 }} inputMode="numeric" maxLength={6} value={code} placeholder={uiText('请输入 6 位动态令牌')}
        onChange={(value) => setCode(value.replace(/\D/g, '').slice(0, 6))} />
      <Button style={{ marginTop: 16 }} type="primary" long loading={loading} onClick={confirm}>{uiText('确认配置')}</Button>
    </div> : <div>
      <Typography.Title heading={6}><IconSafe /> {uiText('安全配置')}</Typography.Title>
      <Space style={{ width: '100%', justifyContent: 'space-between' }}>
        <div><div>{uiText('动态令牌')} <Tag color={status?.configured ? 'green' : 'gray'}>{uiText(status?.configured ? '已配置' : '未配置')}</Tag></div>
          <Typography.Text type="secondary">{status?.enabled ? uiText('使用验证器应用保护账号登录。') : uiText('系统暂未启用登录动态令牌。')}</Typography.Text></div>
        <Button type="primary" loading={loading} disabled={!status?.enabled || !status?.allowed} onClick={begin}>{uiText(status?.configured ? '重新配置' : '去配置')}</Button>
      </Space>
      {status?.enabled && !status?.allowed && <Typography.Paragraph type="secondary">{uiText('当前用户组没有使用登录动态令牌的权限。')}</Typography.Paragraph>}
    </div>}
  </Modal>;
}
