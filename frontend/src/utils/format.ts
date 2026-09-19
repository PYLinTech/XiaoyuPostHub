import uiText from './uiText';

export function formatBytes(value?: number | null) {
  if (value == null) return '-';
  if (value === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    units.length - 1
  );
  return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`;
}

export function formatTime(value?: string) {
  return value
    ? new Date(value).toLocaleString(
        document.documentElement.lang === 'en-US' ? 'en-US' : 'zh-CN',
        { hour12: false }
      )
    : '-';
}

export function formatLimit(value?: number | null) {
  return value == null ? uiText('不限') : value.toLocaleString();
}

/**
 * 取件码有效期文案（只读展示系统配置）。
 * seconds 为 undefined 表示尚未读取到系统设置，null 表示系统配置为永久有效。
 */
export function formatPickupLifetime(seconds?: number | null) {
  if (seconds === undefined) return '-';
  if (seconds == null) return uiText('当前系统配置取件码：永久有效');
  const days = Math.floor(seconds / 86400);
  const remainingHours = (seconds % 86400) / 3600;
  const hours = Number.isInteger(remainingHours)
    ? remainingHours
    : Number(remainingHours.toFixed(2));
  const duration =
    days > 0
      ? `${days} ${uiText('天')}${hours > 0 ? ` ${hours} ${uiText('小时')}` : ''}`
      : `${hours} ${uiText('小时')}`;
  return `${uiText('取件码生成后')} ${duration} ${uiText('内有效')}`;
}
