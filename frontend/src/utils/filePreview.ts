const blockedPreviewExtensions = new Set([
  'md',
  'markdown',
  'html',
  'htm',
  'xhtml',
  'svg',
  'xml',
  'epub',
  'eml',
  'mhtml',
  'mht',
]);

let fileViewerModulePromise:
  | Promise<typeof import('@file-viewer/react-legacy-full')>
  | undefined;

export function loadFileViewer() {
  if (!fileViewerModulePromise) {
    fileViewerModulePromise = import('@file-viewer/react-legacy-full').then(
      (fileViewerModule) => {
        fileViewerModule.setDefaultFullAssetBaseUrl('/libs/file-viewer/');
        return fileViewerModule;
      }
    );
  }
  return fileViewerModulePromise;
}

export async function supportsFilePreview(fileName: string) {
  const extension = fileName.split('.').pop()?.trim().toLowerCase();
  if (!extension || extension === fileName.toLowerCase()) return false;

  // 这些格式可携带主动内容、外链或复杂嵌套解析器。下载仍然可用，但不把
  // 用户上传内容交给当前预览器执行。
  if (blockedPreviewExtensions.has(extension)) return false;

  const { fileViewerFullPreset } = await loadFileViewer();
  return fileViewerFullPreset.renderers.some((renderer) =>
    renderer.definitions?.some((definition) =>
      definition.extensions.some(
        (candidate) => candidate.replace(/^\./, '').toLowerCase() === extension
      )
    )
  );
}
