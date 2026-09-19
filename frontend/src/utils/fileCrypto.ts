/**
 * 浏览器端文件解密（与后端 backend/blobstore/crypto.go 严格对称）。
 *
 * 密钥协商：每次请求现场生成临时 RSA-OAEP-2048 密钥对（用完即弃、不落盘、
 * 刷新即换），公钥经 X-XPH-Client-Public-Key 请求头发给服务端；服务端只下发
 * 用该公钥加密的 DEK 信封，私钥始终不出浏览器内存。
 *
 * 内容解密：AES-256-GCM 分块（默认 4MiB/块），块 nonce = 文件级 nonce（8 字节）
 * 与块序号（4 字节大端）拼接；块密文 = 明文 + 16 字节 tag。固定块大小让进度、
 * Range 与断点续传在密文上仍然可用。
 */

import { blobSHA256 } from '@/utils/sha256';
import uiText from '@/utils/uiText';

export interface EncryptionMeta {
  algorithm: string;
  chunkSize: number;
  fileNonce: string;
  keyId: string;
  keyEnvelope: string;
  sizeBytes: number;
  wireSize: number;
}

export const ENCRYPTION_META_HEADER = 'x-xph-encryption';
export const CLIENT_PUBLIC_KEY_HEADER = 'X-XPH-Client-Public-Key';
/** 交付响应头：内容的明文 SHA-256（服务端不做交付前校验，由接收端自行校验）。 */
export const CONTENT_SHA256_HEADER = 'x-xph-content-sha256';

/**
 * 接收端校验：比对下载内容的明文 SHA-256。
 * expected 为空（例如 zip 制品、第三方直跳）时跳过；不一致时抛错，调用方应
 * 拒绝保存并提示用户重试。
 */
export async function verifyContentSHA256(
  blob: Blob,
  expected?: string | null
): Promise<void> {
  const target = (expected || '').trim().toLowerCase();
  if (!target) return;
  const actual = await blobSHA256(blob);
  if (actual !== target) {
    throw new Error(uiText('文件校验失败：内容与服务器记录不一致，请重试'));
  }
}

const ALGORITHM = 'aes-256-gcm-chunked';
const GCM_TAG_BYTES = 16;

/** Web Crypto 是否可用：非安全上下文（HTTP 部署）下浏览器不提供 subtle。 */
export function encryptionSupported(): boolean {
  return (
    typeof crypto !== 'undefined' &&
    typeof crypto.subtle !== 'undefined' &&
    window.isSecureContext
  );
}

/** 生成临时 RSA-OAEP 密钥对（ECDH 不适用：服务端只做 RSA-OAEP 加密）。 */
export async function createClientKeyPair(): Promise<CryptoKeyPair> {
  return crypto.subtle.generateKey(
    {
      name: 'RSA-OAEP',
      modulusLength: 2048,
      publicExponent: new Uint8Array([1, 0, 1]),
      hash: 'SHA-256',
    },
    false,
    ['encrypt', 'decrypt']
  );
}

/** 导出公钥为 base64(SPKI DER)，用于请求头。 */
export async function exportPublicKey(pair: CryptoKeyPair): Promise<string> {
  const der = await crypto.subtle.exportKey('spki', pair.publicKey);
  return bytesToBase64(new Uint8Array(der));
}

/**
 * 生成临时密钥对与请求头（每次交付请求现场调用，用完即弃）。
 * 非安全上下文（HTTP 部署）返回空头，此时服务端会走"代理解密"路径。
 */
export async function clientKeyHeaders(): Promise<{
  pair: CryptoKeyPair | null;
  headers: Record<string, string>;
}> {
  if (!encryptionSupported()) {
    return { pair: null, headers: {} };
  }
  const pair = await createClientKeyPair();
  const publicKey = await exportPublicKey(pair);
  return { pair, headers: { [CLIENT_PUBLIC_KEY_HEADER]: publicKey } };
}

/**
 * 解析响应头中的加密元数据。
 * 无响应头返回 null；响应头存在但无法解析时必须抛错——把"解析失败"当成
 * "未加密"会导致把密文当明文保存/预览。
 */
export function parseEncryptionHeader(
  header: string | null | undefined
): EncryptionMeta | null {
  if (!header) return null;
  let meta: EncryptionMeta;
  try {
    const json = new TextDecoder().decode(base64ToBytes(header));
    meta = JSON.parse(json) as EncryptionMeta;
  } catch {
    throw new Error(uiText('加密元数据无法解析，请刷新页面后重试'));
  }
  if (
    !meta ||
    meta.algorithm !== ALGORITHM ||
    !meta.fileNonce ||
    !meta.keyEnvelope ||
    typeof meta.chunkSize !== 'number' ||
    typeof meta.sizeBytes !== 'number'
  ) {
    throw new Error(uiText('加密元数据不完整，请刷新页面后重试'));
  }
  return meta;
}

/**
 * 解密分块密文为明文 Blob。
 * 密文布局：块1密文 ‖ 块2密文 ‖ ...，每块 = 明文(≤chunkSize) + 16 字节 tag。
 */
export async function decryptChunked(
  pair: CryptoKeyPair,
  meta: EncryptionMeta,
  cipher: ArrayBuffer,
  mimeType = 'application/octet-stream'
): Promise<Blob> {
  const dek = await crypto.subtle.decrypt(
    { name: 'RSA-OAEP' },
    pair.privateKey,
    base64ToBytes(meta.keyEnvelope)
  );
  const key = await crypto.subtle.importKey('raw', dek, 'AES-GCM', false, [
    'decrypt',
  ]);
  if (meta.sizeBytes < 0) {
    throw new Error(uiText('加密元数据的明文长度无效'));
  }
  const nonceBase = base64ToBytes(meta.fileNonce);
  const chunkSize = meta.chunkSize || 4 * 1024 * 1024;
  const wire = new Uint8Array(cipher);
  if (meta.wireSize && wire.byteLength < meta.wireSize) {
    throw new Error(uiText('密文长度与元数据不符，文件可能不完整'));
  }
  const parts: BlobPart[] = [];
  let index = 0;
  let offset = 0;
  let remaining = meta.sizeBytes;
  while (remaining > 0) {
    const plainLen = Math.min(chunkSize, remaining);
    const wireLen = plainLen + GCM_TAG_BYTES;
    const nonce = new Uint8Array(12);
    nonce.set(nonceBase, 0);
    new DataView(nonce.buffer).setUint32(8, index, false);
    const plain = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: nonce },
      key,
      wire.subarray(offset, offset + wireLen)
    );
    parts.push(plain);
    offset += wireLen;
    remaining -= plainLen;
    index += 1;
  }
  return new Blob(parts, { type: mimeType });
}

/**
 * 用准备好的加密元数据解密：用于「302 直链 + 浏览器端解密」场景——密文取自
 * 第三方直链（响应头由第三方控制，无法携带我们的元数据），因此密钥信封随
 * 准备响应（JSON）一并下发。
 */
export async function decryptWithMeta(
  pair: CryptoKeyPair | null,
  meta: EncryptionMeta,
  cipher: ArrayBuffer,
  mimeType = 'application/octet-stream'
): Promise<Blob> {
  if (!pair) {
    throw new Error(uiText('该文件需要在浏览器端解密，但当前环境不支持'));
  }
  return decryptChunked(pair, meta, cipher, mimeType);
}

/**
 * 解码一次交付响应：按加密元数据解密为 Blob（无元数据即明文交付），可选按
 * 明文 SHA-256 校验。
 *
 * 四条交付路径共用（文件页下载、文件页预览、分享页预览、分享页下载及其 302
 * 直链分支）——请求方式（GET/POST、进度回调、额外请求头）仍由各调用方自理，
 * 这里只统一"解密 → 组装 → 校验"这段数据变换。
 *
 * 加密元数据默认取响应头；302 直链场景下响应头由第三方控制，密钥信封来自准备
 * 响应，此时经 options.meta 传入（校验值同理用 expectedSHA256）。
 */
export async function decodeDelivery(
  pair: CryptoKeyPair | null,
  headers: Record<string, unknown>,
  data: ArrayBuffer,
  options?: {
    meta?: EncryptionMeta | null;
    expectedSHA256?: string | null;
    verifySHA256?: boolean;
  }
): Promise<Blob> {
  const contentType =
    (headers['content-type'] as string) || 'application/octet-stream';
  const meta =
    options?.meta ??
    parseEncryptionHeader(headers[ENCRYPTION_META_HEADER] as string | undefined);
  const blob = meta
    ? await decryptWithMeta(pair, meta, data, contentType)
    : new Blob([data], { type: contentType });
  if (options?.verifySHA256) {
    const expected =
      options.expectedSHA256 ||
      (headers[CONTENT_SHA256_HEADER] as string | undefined);
    await verifyContentSHA256(blob, expected);
  }
  return blob;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  const step = 0x8000;
  for (let i = 0; i < bytes.length; i += step) {
    binary += String.fromCharCode(...bytes.subarray(i, i + step));
  }
  return window.btoa(binary);
}

function base64ToBytes(value: string): Uint8Array {
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}
