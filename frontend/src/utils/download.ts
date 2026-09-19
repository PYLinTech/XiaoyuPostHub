/**
 * 触发浏览器保存一个 Blob。
 *
 * 下载场景的唯一入口：各页面（文件页、分享页、审核页）不再各写一份锚点逻辑，
 * 统一处理隐藏锚点与 ObjectURL 回收。
 */
export function downloadBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = name;
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}
