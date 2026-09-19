import React from 'react';
import { Button, Typography } from '@arco-design/web-react';
import uiText from '@/utils/uiText';

const { Title, Text, Paragraph } = Typography;

interface Props {
  children: React.ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * 页面级错误边界。
 *
 * 背景：任一页面在**渲染期**抛错（例如 UI 组件库 API 用法不符导致的
 * `xxx is not a function`）时，React 会卸载整棵树 —— 用户看到的是白屏，
 * 且看不出哪里出了问题。加上边界后只替换出错的那一块，其它路由与后台
 * 功能不受影响。
 *
 * 说明：
 *   - 只覆盖渲染期错误；事件回调、Promise、定时器里的异常仍须各自 try/catch；
 *   - 错误信息同时打印到 console（含组件栈），便于排查与反馈；
 *   - 由调用方以 pathname 作 key，切换路由即自动复位（见 index.tsx）。
 */
export default class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error('[XiaoyuPostHub] 页面渲染失败：', error, info?.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) {
      return this.props.children;
    }
    return (
      <div style={{ maxWidth: 720, margin: '64px auto', padding: '0 24px' }}>
        <Title heading={4}>{uiText('页面出错了')}</Title>
        <Text type="secondary">
          {uiText('该页面渲染失败，其它页面与后台功能不受影响；刷新即可重试。')}
        </Text>
        <div style={{ marginTop: 16, display: 'flex', gap: 8 }}>
          <Button type="primary" onClick={() => window.location.reload()}>
            {uiText('刷新页面')}
          </Button>
          <Button
            onClick={() => {
              window.location.href = '/';
            }}
          >
            {uiText('返回首页')}
          </Button>
        </div>
        <Paragraph style={{ marginTop: 20, marginBottom: 0 }}>
          <Text
            code
            style={{ wordBreak: 'break-all', whiteSpace: 'pre-wrap', fontSize: 12 }}
          >
            {String(error?.message || error)}
          </Text>
        </Paragraph>
      </div>
    );
  }
}
