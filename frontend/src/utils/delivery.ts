/**
 * 下载计划消费（前端接收定稿）：
 *   服务端只负责鉴权、发地址与计数；浏览器负责取数、解密、合并、打包。
 *   全程串行——一次只取一个文件、一片，避免内存与带宽被同时放大。
 *
 * 取数方式（由服务端下发 dataSource 决定）：
 *   - proxy：从本机中转拉取（加密对象为密文 + 密钥信封，无公钥时是服务器
 *     解密后的明文）；
 *   - redirect：逐片按需取址后直连第三方（单片同样走这条路径）。
 */

import axios from 'axios';
import {
  EncryptionMeta,
  decryptChunked,
  decryptParts,
  verifyContentSHA256,
} from '@/utils/fileCrypto';
import { downloadBlob } from '@/utils/download';
import uiText from '@/utils/uiText';

export interface DeliveryPart {
  index: number;
  plainSize: number;
}

export interface DeliveryItem {
  kind: 'file' | 'folder';
  resourceId: string;
  name: string;
  relativePath?: string;
  sizeBytes: number;
  sha256?: string;
  mimeType?: string;
  parts?: DeliveryPart[];
  encryption?: EncryptionMeta | null;
  streamUrl?: string;
  partUrl?: string;
}

export interface DownloadPlan {
  dataSource: 'proxy' | 'redirect';
  archiveName?: string;
  totalBytes?: number;
  items: DeliveryItem[];
}

export type ProgressHandler = (percent: number) => void;

/** 取一个文件项的明文内容：取数 → 解密 → 明文哈希校验。 */
export async function fetchDeliveryItem(
  pair: CryptoKeyPair | null,
  item: DeliveryItem,
  onProgress?: (loaded: number, total: number) => void
): Promise<Blob> {
  if (item.kind !== 'file') {
    throw new Error(uiText('没有可下载的文件'));
  }
  const mimeType = item.mimeType || 'application/octet-stream';
  if (item.streamUrl) {
    // 本机中转：一次性拉取（密文或明文兜底），前端按块解密。
    const response = await axios.get(item.streamUrl, {
      responseType: 'arraybuffer',
      onDownloadProgress: (event) => {
        if (event.total) onProgress?.(event.loaded, event.total);
      },
    });
    const blob = item.encryption
      ? await decryptChunked(
          pair,
          item.encryption,
          response.data as ArrayBuffer,
          mimeType
        )
      : new Blob([response.data as ArrayBuffer], { type: mimeType });
    await verifyContentSHA256(blob, item.sha256);
    return blob;
  }
  // 302 取数：逐片按需取址 → 逐片拉取 → 逐片解密（单片同样如此）。
  const parts = item.parts || [];
  if (!item.partUrl || !parts.length) {
    throw new Error(uiText('下载失败，请稍后重试'));
  }
  const payloads: ArrayBuffer[] = [];
  for (let index = 0; index < parts.length; index += 1) {
    const part = parts[index];
    const address = await axios.get(`${item.partUrl}${part.index}`);
    const url = (address.data as { url?: string })?.url;
    if (!url) {
      throw new Error(uiText('下载失败，请稍后重试'));
    }
    const response = await axios.get(url, { responseType: 'arraybuffer' });
    payloads.push(response.data as ArrayBuffer);
    onProgress?.(index + 1, parts.length);
  }
  const blob = item.encryption
    ? await decryptParts(pair, item.encryption, parts, payloads, mimeType)
    : new Blob(payloads, { type: mimeType });
  await verifyContentSHA256(blob, item.sha256);
  return blob;
}

/**
 * 下载整个计划：单文件保存为文件；多文件/文件夹串行打包为 ZIP
 * （逐个取数、解密后立即写入压缩包，不整包驻留内存）。
 */
export async function downloadPlan(
  plan: DownloadPlan,
  pair: CryptoKeyPair | null,
  onProgress?: ProgressHandler
): Promise<void> {
  const files = plan.items.filter((item) => item.kind === 'file');
  if (!files.length) {
    throw new Error(uiText('没有可下载的文件'));
  }
  if (!plan.archiveName && files.length === 1) {
    const item = files[0];
    const blob = await fetchDeliveryItem(pair, item, (loaded, total) => {
      if (total) onProgress?.(Math.round((loaded * 100) / total));
    });
    downloadBlob(blob, item.name);
    return;
  }
  const { default: JSZip } = await import('jszip');
  const zip = new JSZip();
  plan.items
    .filter((item) => item.kind === 'folder')
    .forEach((item) => zip.folder(item.relativePath || item.name));
  for (let index = 0; index < files.length; index += 1) {
    const item = files[index];
    const blob = await fetchDeliveryItem(pair, item);
    zip.file(item.relativePath || item.name, blob);
    onProgress?.(Math.round(((index + 1) * 90) / files.length));
  }
  const archive = await zip.generateAsync({ type: 'blob' }, ({ percent }) =>
    onProgress?.(90 + Math.round(percent * 0.1))
  );
  downloadBlob(archive, plan.archiveName || 'download.zip');
}
