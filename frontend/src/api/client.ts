import axios, { AxiosError } from 'axios';
import { uiServerText } from '@/utils/uiText';

/**
 * 全局接口客户端。导入本模块即完成响应拦截注册，所有接口调用共用同一个
 * axios 实例，避免每个页面各自处理会话失效与错误消息。
 *
 * 具体接口按模块定义在 ./endpoints，业务代码从那里取用，不再直接拼 URL。
 */
axios.interceptors.response.use(undefined, (error) => {
  if (typeof error?.response?.data?.msg === 'string') {
    error.response.data.msg = uiServerText(error.response.data.msg);
  }
  const path = window.location.pathname;
  if (
    error?.response?.status === 401 &&
    path !== '/login' &&
    path !== '/m' &&
    !path.startsWith('/s/')
  ) {
    window.location.replace('/login');
  }
  return Promise.reject(error);
});

/**
 * 从接口错误中提取可直接展示给用户的消息。响应拦截器已经把服务端消息
 * 转译为当前语言，这里只负责缺省回退，替代各处重复的取值表达式。
 */
export function apiErrorMessage(error: unknown, fallback: string) {
  const message = (error as AxiosError<{ msg?: string }>)?.response?.data?.msg;
  return typeof message === 'string' && message ? message : fallback;
}

export default axios;
