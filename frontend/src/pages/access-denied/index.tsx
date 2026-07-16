import React from 'react';
import { Button, Result } from '@arco-design/web-react';
import { useHistory } from 'react-router-dom';
import uiText from '@/utils/uiText';
export default function AccessDenied() {
  const history = useHistory();
  return (
    <Result
      status="403"
      title={uiText('无法访问此页面')}
      subTitle={uiText('当前身份没有对应权限，或页面地址不存在。')}
      extra={
        <Button type="primary" onClick={() => history.replace('/')}>
          {uiText('返回首页')}
        </Button>
      }
    />
  );
}
