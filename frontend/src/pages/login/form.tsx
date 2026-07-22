import { Form, Input, Button, Space, Message } from '@arco-design/web-react';
import { FormInstance } from '@arco-design/web-react/es/Form';
import { IconLock, IconUser } from '@arco-design/web-react/icon';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import axios from 'axios';
import useLocale from '@/utils/useLocale';
import locale from './locale';
import styles from './style/index.module.less';
import uiText from '@/utils/uiText';
export default function LoginForm() {
  const formRef = useRef<FormInstance>();
  const [errorMessage, setErrorMessage] = useState('');
  const [loading, setLoading] = useState(false);
  const [challengeToken, setChallengeToken] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [registerMode, setRegisterMode] = useState(false);
  const [registrationRequiresInvitation, setRegistrationRequiresInvitation] =
    useState<boolean | null>(null);
  const [invitationCodePolicy, setInvitationCodePolicy] = useState({
    length: 8,
    caseSensitive: false,
    includeLetters: true,
    includeNumbers: true,
  });
  const t = useLocale(locale);
  function login(params) {
    setErrorMessage('');
    setLoading(true);
    axios
      .post('/api/user/login', params)
      .then((res) => {
        const { status, msg } = res.data;
        if (status === 'totp_required') {
          setChallengeToken(res.data.challengeToken || '');
          setTotpCode('');
        } else if (status === 'ok') {
          window.location.replace('/files');
        } else {
          setErrorMessage(msg || t['login.form.login.errMsg']);
        }
      })
      .catch((error) => {
        const msg = error?.response?.data?.msg;
        setErrorMessage(msg || t['login.form.login.errMsg']);
      })
      .finally(() => {
        setLoading(false);
      });
  }
  function verifyTOTP() {
    if (totpCode.length !== 6) {
      setErrorMessage(uiText('请输入 6 位动态令牌'));
      return;
    }
    setLoading(true);
    setErrorMessage('');
    axios.post('/api/user/login/totp', { challengeToken, code: totpCode })
      .then(() => window.location.replace('/files'))
      .catch((error) => setErrorMessage(error?.response?.data?.msg || uiText('动态令牌验证失败')))
      .finally(() => setLoading(false));
  }
  useEffect(() => {
    if (challengeToken && totpCode.length === 6 && !loading) verifyTOTP();
    // Only a code edit should trigger an automatic verification attempt.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [totpCode]);
  function onSubmitClick() {
    formRef.current.validate().then((values) => {
      if (!registerMode) {
        login(values);
        return;
      }
      if (values.password !== values.confirmPassword) {
        setErrorMessage(t['login.form.confirmPassword.notMatch']);
        return;
      }
      setErrorMessage('');
      setLoading(true);
      axios
        .post('/api/user/register', {
          userName: values.userName,
          password: values.password,
          invitationCode: values.invitationCode || '',
        })
        .then(() => {
          Message.success(t['login.form.register.success']);
          setRegisterMode(false);
          formRef.current.setFieldsValue({
            password: '',
            confirmPassword: '',
            invitationCode: '',
          });
        })
        .catch((error) => {
          setErrorMessage(
            error?.response?.data?.msg || t['login.form.register.errMsg']
          );
          refreshRegistrationSettings();
        })
        .finally(() => setLoading(false));
    });
  }
  const refreshRegistrationSettings = useCallback(() => {
    return axios
      .get('/api/user/registration-settings')
      .then((res) => {
        setRegistrationRequiresInvitation(
          !!res.data.registrationRequiresInvitation
        );
        setInvitationCodePolicy({
          length: Number(res.data.invitationCodeLength) || 8,
          caseSensitive: !!res.data.invitationCodeCaseSensitive,
          includeLetters: res.data.invitationCodeIncludeLetters !== false,
          includeNumbers: res.data.invitationCodeIncludeNumbers !== false,
        });
      })
      .catch(() => undefined);
  }, []);
  useEffect(() => {
    refreshRegistrationSettings();
  }, [refreshRegistrationSettings]);

  // 注册页保持同步管理端策略；管理员切换后，无需刷新页面即可在必填与选填间更新。
  useEffect(() => {
    if (!registerMode) return undefined;
    refreshRegistrationSettings();
    const timer = window.setInterval(refreshRegistrationSettings, 3000);
    return () => window.clearInterval(timer);
  }, [registerMode, refreshRegistrationSettings]);
  const toggleRegisterMode = () => {
    setErrorMessage('');
    setRegisterMode((current) => !current);
    formRef.current?.resetFields();
  };
  return (
    <div className={styles['login-form-wrapper']}>
      {challengeToken ? <>
        <div className={styles['login-form-title']}>{uiText('验证登录动态令牌')}</div>
        <div className={styles['login-form-sub-title']}>{uiText('打开验证器应用，输入当前显示的 6 位动态令牌。')}</div>
        <div className={styles['login-form-error-msg']}>{errorMessage}</div>
        <div className={styles['totp-code']}>
          {Array.from({ length: 6 }).map((_, index) => <span key={index}>{totpCode[index] || ''}</span>)}
          <input autoFocus inputMode="numeric" maxLength={6} value={totpCode}
            aria-label={uiText('6 位动态令牌')}
            onChange={(event) => setTotpCode(event.target.value.replace(/\D/g, '').slice(0, 6))}
            onKeyDown={(event) => { if (event.key === 'Enter') verifyTOTP(); }} />
        </div>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Button type="primary" long loading={loading} onClick={verifyTOTP}>{uiText('验证并登录')}</Button>
          <Button type="text" long onClick={() => { setChallengeToken(''); setErrorMessage(''); }}>{uiText('返回账号登录')}</Button>
        </Space>
      </> : <>
      <div className={styles['login-form-title']}>
        {registerMode ? t['login.form.register.title'] : t['login.form.title']}
      </div>
      <div className={styles['login-form-sub-title']}>
        {registerMode
          ? t['login.form.register.subTitle']
          : t['login.form.subTitle']}
      </div>
      <div className={styles['login-form-error-msg']}>{errorMessage}</div>
      <Form className={styles['login-form']} layout="vertical" ref={formRef}>
        <Form.Item
          field="userName"
          rules={[
            {
              required: true,
              message: t['login.form.userName.errMsg'],
            },
          ]}
        >
          <Input
            prefix={<IconUser />}
            placeholder={t['login.form.userName.placeholder']}
            onPressEnter={onSubmitClick}
          />
        </Form.Item>
        <Form.Item
          field="password"
          rules={[
            {
              required: true,
              message: t['login.form.password.errMsg'],
            },
          ]}
        >
          <Input.Password
            prefix={<IconLock />}
            placeholder={t['login.form.password.placeholder']}
            onPressEnter={onSubmitClick}
          />
        </Form.Item>
        {registerMode && (
          <Form.Item
            field="confirmPassword"
            rules={[
              {
                required: true,
                message: t['login.form.confirmPassword.errMsg'],
              },
            ]}
          >
            <Input.Password
              prefix={<IconLock />}
              placeholder={t['login.form.confirmPassword.placeholder']}
              onPressEnter={onSubmitClick}
            />
          </Form.Item>
        )}
        {registerMode && (
          <Form.Item
            field="invitationCode"
            label={
              registrationRequiresInvitation
                ? t['login.form.invitationCode.requiredLabel']
                : t['login.form.invitationCode.optionalLabel']
            }
            normalize={(value = '') =>
              invitationCodePolicy.caseSensitive ? value : value.toUpperCase()
            }
            rules={[
              ...(registrationRequiresInvitation
                ? [
                    {
                      required: true,
                      message: t['login.form.invitationCode.errMsg'],
                    },
                  ]
                : []),
              {
                validator: (value: string, callback) => {
                  if (!value) return;
                  const letterRange = invitationCodePolicy.caseSensitive
                    ? 'A-Za-z'
                    : 'A-Z';
                  const characters = `${
                    invitationCodePolicy.includeLetters ? letterRange : ''
                  }${invitationCodePolicy.includeNumbers ? '0-9' : ''}`;
                  const valid = new RegExp(
                    `^[${characters}]{${invitationCodePolicy.length}}$`
                  ).test(value);
                  if (!valid) callback(uiText('邀请码格式不正确'));
                },
              },
            ]}
          >
            <Input
              maxLength={invitationCodePolicy.length}
              placeholder={
                registrationRequiresInvitation
                  ? t['login.form.invitationCode.requiredPlaceholder']
                  : t['login.form.invitationCode.placeholder']
              }
              onPressEnter={onSubmitClick}
            />
          </Form.Item>
        )}
        <Space size={16} direction="vertical">
          <Button
            type="primary"
            long
            onClick={onSubmitClick}
            loading={loading}
            disabled={registerMode && registrationRequiresInvitation === null}
          >
            {registerMode
              ? t['login.form.register.submit']
              : t['login.form.login']}
          </Button>
          <Button
            type="text"
            long
            className={styles['login-form-register-btn']}
            onClick={toggleRegisterMode}
          >
            {registerMode
              ? t['login.form.backToLogin']
              : t['login.form.register']}
          </Button>
        </Space>
      </Form>
      </>}
    </div>
  );
}
