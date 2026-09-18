#!/usr/bin/env node
// copy-viewer-assets.mjs — 只复制裁剪后预览器实际需要的运行时资源。
//
// XiaoyuPostHub 只预览图片、文本、Word、Excel、PPT（含 PowerPoint 97–2003）
// 与 PDF，因此不再复制 CAD（wasm/cad）、Typst（wasm/typst）、DrawIO
// （vendor/drawio）、压缩包（vendor/libarchive）等已移除格式的资源。
// 资源来源是随发布锁定的 file-viewer-copy-assets 包，保证与预览器版本一致。
//
// 用法：
//   node scripts/copy-viewer-assets.mjs [目标目录]
// 默认目标目录：build/libs/file-viewer

import { cp, mkdir, readdir, rm, stat } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, resolve, sep } from 'node:path';

const require = createRequire(import.meta.url);

// 每个保留的渲染器与其必需的资源子目录（相对 viewer 根目录）。
// vendor/ppt 是 PowerPoint 97–2003 的独立原生运行时。
const rendererAssetDirectories = [
  { renderer: 'renderer-word', directory: 'vendor/docx' },
  { renderer: 'renderer-pdf', directory: 'vendor/pdf' },
  { renderer: 'renderer-presentation', directory: 'vendor/ppt' },
  { renderer: 'renderer-presentation', directory: 'vendor/pptx' },
  { renderer: 'renderer-spreadsheet', directory: 'vendor/xlsx' },
];

// 主入口文件缺失时直接让构建失败，避免发布不可用的预览器。
const requiredFiles = [
  'vendor/docx/docx.worker.js',
  'vendor/docx/jszip.min.js',
  'vendor/pdf/pdf.worker.mjs',
  'vendor/ppt/index.mjs',
  'vendor/ppt/worker.mjs',
  'vendor/ppt/frame-cache.mjs',
  'vendor/ppt/ppt-native.wasm',
  'vendor/ppt/ppt-font-cjk.otf',
  'vendor/ppt/manifest.json',
  'vendor/pptx/pptx.worker.js',
  'vendor/xlsx/sheet.worker.js',
];

function resolvePackageDirectory() {
  try {
    return dirname(require.resolve('file-viewer-copy-assets/package.json'));
  } catch {
    return resolve(dirname(require.resolve('file-viewer-copy-assets')), '..');
  }
}

async function directorySize(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const sizes = await Promise.all(
    entries.map(async (entry) => {
      const path = resolve(directory, entry.name);
      if (entry.isDirectory()) return directorySize(path);
      return (await stat(path)).size;
    })
  );
  return sizes.reduce((total, size) => total + size, 0);
}

function formatBytes(bytes) {
  const units = ['B', 'KiB', 'MiB', 'GiB'];
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value.toFixed(unitIndex === 0 ? 0 : 1)} ${units[unitIndex]}`;
}

async function main() {
  const targetArgument = process.argv[2] || 'build/libs/file-viewer';
  const workspaceRoot = process.cwd();
  const targetDir = resolve(workspaceRoot, targetArgument);

  if (!targetDir.startsWith(`${workspaceRoot}${sep}`)) {
    throw new Error(`目标目录必须位于项目目录内：${targetArgument}`);
  }

  const sourceDir = resolve(resolvePackageDirectory(), 'viewer');
  await stat(sourceDir).catch(() => {
    throw new Error(`缺少预览器资源目录：${sourceDir}`);
  });

  await rm(targetDir, { recursive: true, force: true });
  await mkdir(targetDir, { recursive: true });

  let copiedBytes = 0;
  for (const { renderer, directory } of rendererAssetDirectories) {
    const source = resolve(sourceDir, directory);
    await stat(source).catch(() => {
      throw new Error(`${renderer} 缺少资源子目录：${source}`);
    });
    await cp(source, resolve(targetDir, directory), { recursive: true });
    copiedBytes += await directorySize(resolve(targetDir, directory));
  }

  const missingFiles = [];
  for (const file of requiredFiles) {
    await stat(resolve(targetDir, file)).catch(() => missingFiles.push(file));
  }
  if (missingFiles.length > 0) {
    throw new Error(`预览器资源不完整，缺少：${missingFiles.join(', ')}`);
  }

  console.log(
    `[copy-viewer-assets] 已复制 ${rendererAssetDirectories.length} 个渲染器的资源（${formatBytes(copiedBytes)}）到 ${targetArgument}`
  );
}

main().catch((reason) => {
  console.error(
    `[copy-viewer-assets] ${reason instanceof Error ? reason.message : String(reason)}`
  );
  process.exit(1);
});
