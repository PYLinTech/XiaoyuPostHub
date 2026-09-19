/**
 * 浏览器端文件解密（与后端 backend/blobstore/crypto.go 严格对称）。
 *
 * 交付形态（前端接收定稿）：
 *   - 本机中转：服务端下发连续密文流（请求方无法前端解密时改发明文兜底），
 *     密文按固定块解密；
 *   - 302 取数：前端逐片拉取第三方密文，按片解密后顺序合并（单片同样走这条
 *     路径，不存在单分片特殊分支）。
 *
 * 密钥协商：每次请求现场生成临时 RSA-OAEP-2048 密钥对（用完即弃、不落盘、
 * 刷新即换），公钥经 X-XPH-Client-Public-Key 请求头发给服务端；服务端只下发
 * 用该公钥加密的 DEK 信封，私钥始终不出浏览器内存。
 *
 * 内容解密：AES-256-GCM 分块（默认 4MiB/块），块 nonce = 文件级 nonce（8 字节）
 * 与块序号（4 字节大端）拼接；块密文 = 明文 + 16 字节 tag。固定块大小让块序号
 * 可直接由明文偏移推出，因此逐片解密与合并互不影响。
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
 * 交付响应头：本次下发字节的形态（plaintext | ciphertext），服务端每次内容交付
 * 都会设置。它是前后端固定的显式契约——不再靠"有没有加密元数据头"隐式推断，
 * 否则服务器解密兜底输出明文时会被误当密文解密。
 */
export const CONTENT_FORM_HEADER = 'x-xph-content-form';

export type ContentForm = 'plaintext' | 'ciphertext';

/**
 * 解析本次交付的形态：以固定响应头为准；头缺失（旧服务端）时按是否带加密元数据
 * 推断，保证向后兼容。
 */
export function contentFormOf(
  headers: Record<string, unknown>,
  meta: EncryptionMeta | null
): ContentForm {
  const raw = String(headers[CONTENT_FORM_HEADER] ?? '')
    .trim()
    .toLowerCase();
  if (raw === 'ciphertext') return 'ciphertext';
  if (raw === 'plaintext') return 'plaintext';
  return meta ? 'ciphertext' : 'plaintext';
}

/**
 * 抛出交付错误：用户可见文案保持中性（不出现密文/元数据/协议字段等实现细节），
 * 技术原因只写控制台，便于排查又不打扰使用者。
 */
export function failDelivery(message: string, detail?: string): never {
  if (detail) console.warn('[delivery]', detail);
  throw new Error(uiText(message));
}

/**
 * 接收端校验：比对下载内容的明文 SHA-256。
 * expected 为空（例如明文对象以外的兜底路径）时跳过；不一致时抛错，调用方应
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
    failDelivery('文件校验失败，请重新下载', `sha256 mismatch: got=${actual} want=${target}`);
  }
}

const ALGORITHM = 'aes-256-gcm-chunked';
const GCM_TAG_BYTES = 16;
const DEFAULT_CHUNK_SIZE = 4 * 1024 * 1024;

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
 * 非安全上下文（HTTP 部署）返回空头，此时服务端按「无法前端解密」兜底输出明文。
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
    failDelivery('下载失败，请刷新页面后重试', 'malformed encryption metadata');
  }
  if (
    !meta ||
    meta.algorithm !== ALGORITHM ||
    !meta.fileNonce ||
    !meta.keyEnvelope ||
    typeof meta.chunkSize !== 'number' ||
    typeof meta.sizeBytes !== 'number'
  ) {
    failDelivery('下载失败，请刷新页面后重试', 'incomplete encryption metadata');
  }
  return meta;
}

/**
 * 解开密钥信封并导入 AES-GCM 密钥（本机中转与 302 逐片解密共用）。
 * 无临时密钥对（非安全上下文）时无法解密，必须抛错。
 */
export async function importDeliveryKey(
  pair: CryptoKeyPair | null,
  meta: EncryptionMeta
): Promise<CryptoKey> {
  if (!pair) {
    failDelivery(
      '当前浏览器无法下载该文件，请更换浏览器或联系管理员',
      'client key pair unavailable'
    );
  }
  if (meta.sizeBytes < 0) {
    failDelivery('下载失败，请刷新页面后重试', `invalid plaintext size: ${meta.sizeBytes}`);
  }
  const dek = await crypto.subtle.decrypt(
    { name: 'RSA-OAEP' },
    pair.privateKey,
    base64ToBytes(meta.keyEnvelope)
  );
  return crypto.subtle.importKey('raw', dek, 'AES-GCM', false, ['decrypt']);
}

/** 从 startIndex 块序号开始，按块解密一段密文并返回明文分块。 */
async function decryptWireRange(
  key: CryptoKey,
  nonceBase: Uint8Array,
  chunkSize: number,
  startIndex: number,
  wire: Uint8Array,
  plainLength: number
): Promise<ArrayBuffer[]> {
  const out: ArrayBuffer[] = [];
  let index = startIndex;
  let offset = 0;
  let remaining = plainLength;
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
    out.push(plain);
    offset += wireLen;
    remaining -= plainLen;
    index += 1;
  }
  return out;
}

/**
 * 解密连续密文（本机中转流）。
 * 密文布局：块1密文 ‖ 块2密文 ‖ ...，每块 = 明文(≤chunkSize) + 16 字节 tag。
 */
export async function decryptChunked(
  pair: CryptoKeyPair | null,
  meta: EncryptionMeta,
  cipher: ArrayBuffer,
  mimeType = 'application/octet-stream'
): Promise<Blob> {
  const key = await importDeliveryKey(pair, meta);
  const nonceBase = base64ToBytes(meta.fileNonce);
  const chunkSize = meta.chunkSize || DEFAULT_CHUNK_SIZE;
  const wire = new Uint8Array(cipher);
  if (meta.wireSize && wire.byteLength < meta.wireSize) {
    failDelivery(
      '文件下载不完整，请重试',
      `truncated delivery: got=${wire.byteLength} want=${meta.wireSize}`
    );
  }
  const blocks = await decryptWireRange(
    key,
    nonceBase,
    chunkSize,
    0,
    wire,
    meta.sizeBytes
  );
  return new Blob(blocks, { type: mimeType });
}

/**
 * 解密分片密文（302 逐片取数）：逐片解密后顺序合并。
 * 每片的起始块序号 = 前面各片明文长度之和 ÷ chunkSize（分片边界与加密块对齐，
 * 因此恒为整数）；单片同样是长度 1 的列表，走同一条路径。
 */
export async function decryptParts(
  pair: CryptoKeyPair | null,
  meta: EncryptionMeta,
  parts: Array<{ plainSize: number }>,
  payloads: ArrayBuffer[],
  mimeType = 'application/octet-stream'
): Promise<Blob> {
  const key = await importDeliveryKey(pair, meta);
  const nonceBase = base64ToBytes(meta.fileNonce);
  const chunkSize = meta.chunkSize || DEFAULT_CHUNK_SIZE;
  const out: BlobPart[] = [];
  let startIndex = 0;
  for (let index = 0; index < parts.length; index += 1) {
    const wire = new Uint8Array(payloads[index] || new ArrayBuffer(0));
    const plainLength = Math.max(0, parts[index].plainSize);
    const blocks = await decryptWireRange(
      key,
      nonceBase,
      chunkSize,
      startIndex,
      wire,
      plainLength
    );
    blocks.forEach((block) => out.push(block));
    startIndex += Math.ceil(plainLength / chunkSize);
  }
  return new Blob(out, { type: mimeType });
}

/**
 * 用准备好的加密元数据解密：用于「302 取数」等场景——密文来自第三方（响应头
 * 由第三方控制），因此密钥信封随我们的准备响应（JSON）一并下发。
 */
export async function decryptWithMeta(
  pair: CryptoKeyPair | null,
  meta: EncryptionMeta,
  cipher: ArrayBuffer,
  mimeType = 'application/octet-stream'
): Promise<Blob> {
  return decryptChunked(pair, meta, cipher, mimeType);
}

/**
 * 解码一次交付响应：按加密元数据解密为 Blob（无元数据即明文交付），可选按
 * 明文 SHA-256 校验。用于预览等仍以响应头下发元数据的路径。
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
  const form = contentFormOf(headers, meta);
  if (form === 'ciphertext' && !meta) {
    // 声明密文却没给解密元数据：绝不能按明文保存（会把密文写成坏文件）。
    failDelivery('下载失败，请刷新页面后重试', 'ciphertext declared without key envelope');
  }
  const blob =
    form === 'ciphertext'
      ? await decryptWithMeta(pair, meta as EncryptionMeta, data, contentType)
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
