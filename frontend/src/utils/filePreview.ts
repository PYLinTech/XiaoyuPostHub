import type { ComponentType } from 'react';
import type {
  FileViewerOptions,
  FileViewerRendererPreset,
  RendererDefinition,
} from '@file-viewer/core';
import type { FileViewerLegacyProps } from '@file-viewer/react-legacy';

/**
 * 预览器静态资源地址。构建时由 scripts/copy-viewer-assets.mjs 只复制图片、
 * 文本、Word、Excel、PPT（含 PowerPoint 97–2003）与 PDF 渲染器实际需要的
 * Worker、WASM 与字体资源，不再复制 CAD、Typst、DrawIO 等已移除格式的资源。
 */
const viewerAssetBaseUrl = '/libs/file-viewer/';

/**
 * 这些格式可携带主动内容、外链或复杂嵌套解析器。下载仍然可用，但不把
 * 用户上传内容交给当前预览器执行。
 */
const blockedPreviewExtensions = new Set([
  'md',
  'markdown',
  'html',
  'htm',
  'xhtml',
  'svg',
  'xml',
]);

export interface FileViewerBundle {
  FileViewer: ComponentType<FileViewerLegacyProps>;
  viewerOptions: FileViewerOptions;
  rendererDefinitions: readonly RendererDefinition[];
}

let fileViewerBundlePromise: Promise<FileViewerBundle> | undefined;

function assetUrl(path: string) {
  return `${viewerAssetBaseUrl}${path.replace(/^\/+/, '')}`;
}

/**
 * 只装配图片、文本、Word、Excel、PPT 与 PDF 的渲染器，配合
 * `rendererMode: 'replace'` 让预览能力完全由这里的显式列表决定。
 */
export function loadFileViewer() {
  if (!fileViewerBundlePromise) {
    fileViewerBundlePromise = Promise.all([
      import('@file-viewer/core/assets'),
      import('@file-viewer/react-legacy'),
      import('@file-viewer/renderer-image'),
      import('@file-viewer/renderer-pdf'),
      import('@file-viewer/renderer-presentation'),
      import('@file-viewer/renderer-spreadsheet'),
      import('@file-viewer/renderer-text'),
      import('@file-viewer/renderer-word'),
    ]).then(
      ([
        assets,
        reactLegacy,
        image,
        pdf,
        presentation,
        spreadsheet,
        text,
        word,
      ]) => {
        const renderers = [
          image.imageRenderer,
          text.textRenderer,
          pdf.pdfRenderer,
          presentation.presentationRenderer,
          spreadsheet.spreadsheetRenderer,
          word.wordRenderer,
        ];
        const preset: FileViewerRendererPreset = {
          id: 'xiaoyuposthub-common-formats',
          label: 'XiaoyuPostHub image, text and office renderers',
          renderers,
        };
        const viewerOptions: FileViewerOptions = {
          // replace 模式从空注册表开始，能力集完全由 preset 决定；同时关闭
          // 其他 preset 的自动装配，避免将来引入新的全量预设。
          rendererMode: 'replace',
          autoRenderers: false,
          preset,
          docx: {
            workerUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_DOCX_WORKER_PATH),
            workerJsZipUrl: assetUrl(
              assets.DEFAULT_FILE_VIEWER_DOCX_WORKER_JSZIP_PATH
            ),
          },
          pdf: {
            workerUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PDF_WORKER_PATH),
            cMapUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PDF_CMAP_PATH),
            wasmUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PDF_WASM_PATH),
            standardFontDataUrl: assetUrl(
              assets.DEFAULT_FILE_VIEWER_PDF_STANDARD_FONT_PATH
            ),
            cjkFontFallbackPath: assetUrl(
              assets.DEFAULT_FILE_VIEWER_PDF_CJK_FONT_FALLBACK_PATH
            ),
          },
          presentation: {
            workerUrl: assetUrl(
              assets.DEFAULT_FILE_VIEWER_PRESENTATION_WORKER_PATH
            ),
            // PowerPoint 97–2003（.ppt）使用独立的原生运行时。
            pptModuleUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PPT_MODULE_PATH),
            pptWorkerUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PPT_WORKER_PATH),
            pptWasmUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PPT_WASM_PATH),
            pptFontUrl: assetUrl(assets.DEFAULT_FILE_VIEWER_PPT_FONT_PATH),
          },
          spreadsheet: {
            workerUrl: assetUrl(
              assets.DEFAULT_FILE_VIEWER_SPREADSHEET_WORKER_PATH
            ),
          },
        };
        return {
          FileViewer: reactLegacy.FileViewerLegacy,
          viewerOptions,
          rendererDefinitions: renderers.flatMap(
            (renderer) => renderer.definitions ?? []
          ),
        };
      }
    );
  }
  return fileViewerBundlePromise;
}

export async function supportsFilePreview(fileName: string) {
  const extension = fileName.split('.').pop()?.trim().toLowerCase();
  if (!extension || extension === fileName.toLowerCase()) return false;

  if (blockedPreviewExtensions.has(extension)) return false;

  const { rendererDefinitions } = await loadFileViewer();
  return rendererDefinitions.some((definition) =>
    definition.extensions.some(
      (candidate) => candidate.replace(/^\./, '').toLowerCase() === extension
    )
  );
}
