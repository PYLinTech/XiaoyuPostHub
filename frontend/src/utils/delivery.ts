/**
 * 下载计划消费（前端接收定稿）：
 *   服务端只负责鉴权、发地址与计数；浏览器负责取数、解密、合并、打包。
 *   全程串行——一次只取一个文件、一片，避免内存与带宽被同时放大。
 *
 * 取数方式（由服务端下发 dataSource 决定）：
 *   - proxy：从本机中转拉取（加密对象为密文 + 密钥信封，无法前端解密时是服务器
 *     解密后的明文）；实际形态以响应头 X-XPH-Encryption 为准，不能只看准备响应
 *     里的 encryption（否则会把明文兜底当成密文）；
 *   - redirect：逐片按需取址后直连第三方（单片同样走这条路径）。
 */

import axios from 'axios';
import {
  CLIENT_PUBLIC_KEY_HEADER,
  CONTENT_FORM_HEADER,
  ContentForm,
  EncryptionMeta,
  decodeDelivery,
  decryptParts,
  exportPublicKey,
  failDelivery,
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
  /**
   * 服务端声明的取数形态（固定契约字段）：302 取数时第三方响应头不受我们控制，
   * 只能按它决定逐片解密还是直接拼接；本机中转时以响应头为准。
   */
  contentForm?: ContentForm;
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
    // 本机中转：一次性拉取，按响应头判断实际交付形态（密文 + 密钥信封，或
    // 服务器实时解密后的明文兜底）。
    //
    // 加密对象必须带上临时公钥：服务端「带公钥 → 下发密文 + 信封；无公钥 →
    // 服务器解密兜底输出明文」，而准备响应里的 encryption 是按带公钥的请求生成
    // 的——两条请求的判定必须一致，否则会把明文当密文解密（长度不符报错）。
    const headers =
      item.encryption && pair
        ? { [CLIENT_PUBLIC_KEY_HEADER]: await exportPublicKey(pair) }
        : {};
    const response = await axios.get(item.streamUrl, {
      responseType: 'arraybuffer',
      headers,
      onDownloadProgress: (event) => {
        if (event.total) onProgress?.(event.loaded, event.total);
      },
    });
    // 契约核对：服务端明确声明「明文」时，字节数不可能达到密文长度（密文 = 明文 +
    // 16 字节/加密块）——两边声明矛盾时宁可报错，也不能把密文当明文保存。
    const body = response.data as ArrayBuffer;
    const declared = String(response.headers[CONTENT_FORM_HEADER] ?? '')
      .trim()
      .toLowerCase();
    if (
      declared === 'plaintext' &&
      item.encryption &&
      body.byteLength >= item.encryption.wireSize
    ) {
      failDelivery(
        '下载失败，请刷新页面后重试',
        `content form mismatch: declared=plaintext got=${body.byteLength} wire=${item.encryption.wireSize}`
      );
    }
    return decodeDelivery(pair, response.headers, body, {
      expectedSHA256: item.sha256,
      verifySHA256: true,
    });
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
  // 形态以准备响应的固定字段为准（第三方按存储层原样返回，不额外声明）。
  const form: ContentForm =
    item.contentForm ?? (item.encryption ? 'ciphertext' : 'plaintext');
  if (form === 'ciphertext' && !item.encryption) {
    failDelivery('下载失败，请刷新页面后重试', 'ciphertext part without key envelope');
  }
  const blob =
    form === 'ciphertext'
      ? await decryptParts(
          pair,
          item.encryption as EncryptionMeta,
          parts,
          payloads,
          mimeType
        )
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
