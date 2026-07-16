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
