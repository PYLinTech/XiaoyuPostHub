import React, { useContext, useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { Button, Card, Message, Typography } from '@arco-design/web-react';
import { IconDownload } from '@arco-design/web-react/icon';
import { GlobalContext } from '@/context';
import PublicSharePage from '@/pages/share';
import uiText from '@/utils/uiText';
import shareStyles from '@/pages/share/style/index.module.less';
import styles from './index.module.less';

export default function PickupPage() {
  const { siteName } = useContext(GlobalContext);
  const [length, setLength] = useState(6);
  const [characters, setCharacters] = useState<string[]>(Array(6).fill(''));
  const [code, setCode] = useState('');
  const [lifetimeSeconds, setLifetimeSeconds] = useState<number | null>(3600);
  const [checking, setChecking] = useState(false);
  const inputRefs = useRef<Array<HTMLInputElement | null>>([]);

  const lifetimeText = () => {
    if (lifetimeSeconds == null) return uiText('当前系统配置取件码：永久有效');
    const days = Math.floor(lifetimeSeconds / 86400);
    const remainingHours = (lifetimeSeconds % 86400) / 3600;
    const hours = Number.isInteger(remainingHours)
      ? remainingHours
      : Number(remainingHours.toFixed(2));
    const duration = days > 0
      ? `${days} ${uiText('天')}${hours > 0 ? ` ${hours} ${uiText('小时')}` : ''}`
      : `${hours} ${uiText('小时')}`;
    return `${uiText('取件码生成后')} ${duration} ${uiText('内有效')}`;
  };

  useEffect(() => {
    document.title = `${siteName || 'XiaoyuPostHub'}-${uiText('取件码')}`;
  }, [siteName]);
  useEffect(() => {
    axios.get('/api/site-config').then((response) => {
      const nextLength = Math.max(1, Math.min(64, Number(response.data.pickupCodeLength) || 6));
      setLength(nextLength);
      setLifetimeSeconds(response.data.pickupMaxLifetimeSeconds ?? null);
      const queryCode = new URLSearchParams(window.location.search).get('code') || '';
      const cleanCode = queryCode.replace(/[^a-z0-9]/gi, '').slice(0, nextLength);
      setCharacters(Array.from({ length: nextLength }, (_, index) => cleanCode[index] || ''));
      if (cleanCode.length === nextLength) setCode(cleanCode);
    });
  }, []);

  const applyText = (start: number, text: string) => {
    const values = text.replace(/[^a-z0-9]/gi, '').slice(0, length - start).split('');
    if (!values.length) return;
    setCharacters((current) => {
      const next = [...current];
      values.forEach((value, offset) => { next[start + offset] = value; });
      return next;
    });
    inputRefs.current[Math.min(start + values.length, length - 1)]?.focus();
  };
  const retrieve = async () => {
    const normalized = characters.join('');
    if (normalized.length !== length) return;
    setChecking(true);
    try {
      await axios.get(`/api/pickups/${encodeURIComponent(normalized)}`);
      setCode(normalized);
    } catch (error) {
      Message.error(error?.response?.data?.msg || uiText('取件码无效或已过期'));
    } finally {
      setChecking(false);
    }
  };
  useEffect(() => {
    if (!code && !checking && characters.length === length && characters.every(Boolean)) {
      retrieve();
    }
    // Only a code edit should trigger an automatic retrieval attempt.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [characters]);
  if (code) return <PublicSharePage pickupCode={code} />;
  return (
    <div className={shareStyles.page}>
      <main className={shareStyles.main}>
        <Card className={`${shareStyles['password-card']} ${styles.card}`}>
          <IconDownload className={shareStyles['lock-icon']} />
          <Typography.Title heading={4}>{uiText('输入取件码')}</Typography.Title>
          <Typography.Text type="secondary">{lifetimeText()}</Typography.Text>
          <div className={styles.code} style={{ gridTemplateColumns: `repeat(${Math.min(length, 12)}, minmax(36px, 44px))` }}>
            {characters.map((character, index) => (
              <input
                key={index}
                ref={(node) => { inputRefs.current[index] = node; }}
                className={styles.cell}
                value={character}
                maxLength={1}
                onFocus={(event) => event.target.select()}
                onPaste={(event) => { event.preventDefault(); applyText(index, event.clipboardData.getData('text')); }}
                onChange={(event) => {
                  const clean = event.target.value.replace(/[^a-z0-9]/gi, '').slice(-1);
                  setCharacters((current) => current.map((item, position) => position === index ? clean : item));
                  if (clean && index + 1 < length) inputRefs.current[index + 1]?.focus();
                }}
                onKeyDown={(event) => {
                  if (event.key === 'Backspace' && !characters[index] && index > 0) inputRefs.current[index - 1]?.focus();
                  if (event.key === 'Enter') retrieve();
                }}
              />
            ))}
          </div>
          <Button type="primary" size="large" loading={checking} disabled={characters.some((value) => !value)} onClick={retrieve}>
            {uiText('取件')}
          </Button>
        </Card>
      </main>
    </div>
  );
}
