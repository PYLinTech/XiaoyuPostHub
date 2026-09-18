import { fetchLoginSeal } from '@/api/endpoints';

/**
 * 账号密码类请求的加密信封工具。
 *
 * 服务端每次启动都会在内存中生成 RSA 密钥对，并通过
 * GET /api/user/login/seal 实时下发公钥、一次性 nonce 与本次部署唯一
 * 允许的填充方式；此模块把敏感字段加密成后端可解的信封，明文密码不会
 * 出现在请求体里。
 *
 * 填充方式由服务端配置（HTTPS_ENABLED）固定，前端严格按后端下发的
 * algorithm 选择实现，不按浏览器环境自动回退：
 *   - RSA-OAEP-256 → Web Crypto（要求安全上下文，HTTPS 部署）
 *   - RSA1_5      → 按需加载 jsencrypt（纯 HTTP 部署）
 */

/** 服务端在公钥轮换或 nonce 失效时返回的错误码，表示"可以直接重试"。 */
export const SEAL_STALE_CODE = 'seal_stale';

/**
 * 站点配置为 HTTPS 登录加密，但当前页面不是安全上下文（例如改用 HTTP
 * 地址访问）时抛出：此时浏览器不提供 Web Crypto，无法按配置完成加密。
 */
export class SealEnvironmentError extends Error {
  constructor() {
    super('login seal requires a secure context');
    this.name = 'SealEnvironmentError';
  }
}

type SealMaterial = {
  keyId: string;
  publicKey: string;
  nonce: string;
  algorithm: string;
};

export type SealedEnvelope = {
  keyId: string;
  algorithm: string;
  nonce: string;
  payload: string;
};

function pemToDerBytes(pem: string): Uint8Array {
  const body = pem.replace(/-----[^-]+-----/g, '').replace(/\s+/g, '');
  const binary = window.atob(body);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let index = 0; index < bytes.length; index += 1) {
    binary += String.fromCharCode(bytes[index]);
  }
  return window.btoa(binary);
}

function webCryptoAvailable(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.isSecureContext &&
    Boolean(window.crypto && window.crypto.subtle)
  );
}

async function fetchSealMaterial(): Promise<SealMaterial> {
  const response = await fetchLoginSeal();
  const data = response?.data || {};
  if (
    data.status !== 'ok' ||
    !data.keyId ||
    !data.publicKey ||
    !data.nonce ||
    !data.algorithm
  ) {
    throw new Error('login seal unavailable');
  }
  return {
    keyId: data.keyId,
    publicKey: data.publicKey,
    nonce: data.nonce,
    algorithm: data.algorithm,
  };
}

async function sealWithWebCrypto(publicKey: string, plaintext: Uint8Array) {
  const key = await window.crypto.subtle.importKey(
    'spki',
    pemToDerBytes(publicKey),
    { name: 'RSA-OAEP', hash: 'SHA-256' },
    false,
    ['encrypt']
  );
  const ciphertext = await window.crypto.subtle.encrypt(
    { name: 'RSA-OAEP' },
    key,
    plaintext
  );
  return {
    algorithm: 'RSA-OAEP-256',
    payload: bytesToBase64(new Uint8Array(ciphertext)),
  };
}

async function sealWithJsEncrypt(publicKey: string, plaintext: string) {
  const module = await import('jsencrypt');
  const encryptor = new module.default();
  encryptor.setPublicKey(publicKey);
  const payload = encryptor.encrypt(plaintext);
  if (!payload) {
    throw new Error('login seal encrypt failed');
  }
  return { algorithm: 'RSA1_5', payload };
}

/** 把敏感字段加密成后端可解的信封（每次调用都会取新的公钥与 nonce）。 */
export async function sealCredentialPayload(
  fields: Record<string, unknown>
): Promise<SealedEnvelope> {
  const material = await fetchSealMaterial();
  const plaintext = JSON.stringify(fields);
  if (material.algorithm === 'RSA-OAEP-256') {
    if (!webCryptoAvailable()) {
      throw new SealEnvironmentError();
    }
    const sealed = await sealWithWebCrypto(
      material.publicKey,
      new TextEncoder().encode(plaintext)
    );
    return { keyId: material.keyId, nonce: material.nonce, ...sealed };
  }
  if (material.algorithm === 'RSA1_5') {
    const sealed = await sealWithJsEncrypt(material.publicKey, plaintext);
    return { keyId: material.keyId, nonce: material.nonce, ...sealed };
  }
  throw new Error(`unsupported login seal algorithm: ${material.algorithm}`);
}

/**
 * 登录页预检结果：
 *   - ok：当前页面可以正常完成登录加密；
 *   - needs-https：站点要求 RSA-OAEP，但当前不是安全上下文（例如用 http 地址打开）；
 *   - unknown：预检请求失败，交由提交流程给出提示。
 */
export type SealAvailability = 'ok' | 'needs-https' | 'unknown';

/**
 * 预取服务端加密配置，判断当前页面能否完成登录加密。
 *
 * 登录页在挂载时调用：命中 needs-https 时直接展示引导卡片，避免用户填完
 * 表单才发现提交不了。
 */
export async function detectSealAvailability(): Promise<SealAvailability> {
  try {
    const material = await fetchSealMaterial();
    if (material.algorithm === 'RSA-OAEP-256' && !webCryptoAvailable()) {
      return 'needs-https';
    }
    return 'ok';
  } catch {
    // 预检失败不阻塞登录：提交路径仍会给出对应提示。
    return 'unknown';
  }
}

/**
 * 从当前地址推导同路径的 HTTPS 地址，供引导卡片跳转。
 * 标准端口 80 会被归一化去掉；非标准端口不做猜测，原样保留。
 */
export function securePageUrl(): string {
  const url = new URL(window.location.href);
  url.protocol = 'https:';
  if (url.port === '80') {
    url.port = '';
  }
  return url.toString();
}

/** 判断错误是否为"公钥已轮换 / nonce 失效"，这类失败可以直接重试。 */
export function isSealStale(error: unknown): boolean {
  const data = (error as { response?: { data?: { code?: string } } })?.response?.data;
  return data?.code === SEAL_STALE_CODE;
}

/**
 * 提交一次密封请求。
 *
 * 服务端密钥随进程重启轮换、nonce 一次性，若提交时刚好遇到服务重启或
 * nonce 过期，会自动取回新公钥重试一次，用户无需感知。
 */
export async function submitSealed<T>(
  fields: Record<string, unknown>,
  submit: (envelope: SealedEnvelope) => Promise<T>
): Promise<T> {
  try {
    return await submit(await sealCredentialPayload(fields));
  } catch (error) {
    if (!isSealStale(error)) {
      throw error;
    }
    return submit(await sealCredentialPayload(fields));
  }
}
