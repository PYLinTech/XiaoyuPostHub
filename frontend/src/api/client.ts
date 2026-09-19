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
 *
 * 注意：responseType 为 arraybuffer/blob 的请求，错误体不会经过拦截器转译，
 * 这里对 ArrayBuffer 做同步解码并补一次服务端文案转译。
 */
export function apiErrorMessage(error: unknown, fallback: string) {
  const data = (error as AxiosError<unknown>)?.response?.data;
  if (data instanceof ArrayBuffer) {
    try {
      const parsed = JSON.parse(new TextDecoder().decode(data)) as {
        msg?: string;
      };
      return typeof parsed?.msg === 'string' && parsed.msg
        ? uiServerText(parsed.msg)
        : fallback;
    } catch {
      return fallback;
    }
  }
  const message = (data as { msg?: string })?.msg;
  if (typeof message === 'string' && message) return message;
  // 非接口错误（本地校验/解密失败、网络中断）携带的 message 通常比通用兜底
  // 更有价值；这些消息由抛错方负责本地化（uiText），直接展示。
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

export default axios;
