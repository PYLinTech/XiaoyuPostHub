import React, { useEffect, useRef, useState } from 'react';
import { Spin } from '@arco-design/web-react';
import { FileViewerBundle, loadFileViewer } from '@/utils/filePreview';
interface Props {
  url: string;
  name: string;
  size?: number;
  className?: string;
  /**
   * 是否允许下载。为 false 时预览器工具栏不显示下载按钮，且不会触发
   * onDownload——避免出现点了没有反应的“死按钮”。
   */
  canDownload?: boolean;
  onDownload?: () => void;
  onStateChange?: (state: { error?: unknown }) => void;
}
type ViewerRoot = Document | ShadowRoot | Element;
const dangerousUrlPattern =
  /^\s*(?:javascript|vbscript|data\s*:\s*text\/html)/i;
const navigationAttributes = ['href', 'xlink:href', 'action', 'formaction'];
function isNavigationElement(value: EventTarget) {
  return (
    value instanceof HTMLAnchorElement ||
    value instanceof HTMLAreaElement ||
    (value instanceof Element &&
      (value.getAttribute('role') === 'link' ||
        value.hasAttribute('href') ||
        value.hasAttribute('xlink:href')))
  );
}
function blockNavigation(event: Event) {
  if (
    event.type === 'submit' ||
    event.composedPath().some(isNavigationElement)
  ) {
    event.preventDefault();
    event.stopPropagation();
    event.stopImmediatePropagation();
  }
}
function hardenElement(element: Element) {
  if (
    element.matches('script, object, embed, base, meta[http-equiv="refresh" i]')
  ) {
    element.remove();
    return;
  }
  for (const attribute of Array.from(element.attributes)) {
    if (/^on/i.test(attribute.name)) element.removeAttribute(attribute.name);
  }
  for (const attribute of navigationAttributes) {
    if (element.hasAttribute(attribute)) {
      element.removeAttribute(attribute);
      element.setAttribute('aria-disabled', 'true');
    }
  }
  for (const attribute of ['src', 'poster']) {
    const value = element.getAttribute(attribute);
    if (value && dangerousUrlPattern.test(value)) {
      element.removeAttribute(attribute);
    }
  }
  if (element instanceof HTMLIFrameElement) {
    const currentSandbox = new Set(
      (element.getAttribute('sandbox') || '').split(/\s+/).filter(Boolean)
    );
    const safeSandbox = currentSandbox.has('allow-same-origin')
      ? 'allow-same-origin'
      : '';
    if (element.getAttribute('sandbox') !== safeSandbox) {
      element.setAttribute('sandbox', safeSandbox);
    }
  }
}
function hardenRoot(root: ViewerRoot, listenedTargets: WeakSet<EventTarget>) {
  if (!listenedTargets.has(root)) {
    root.addEventListener('click', blockNavigation, true);
    root.addEventListener('auxclick', blockNavigation, true);
    root.addEventListener('submit', blockNavigation, true);
    listenedTargets.add(root);
  }
  for (const element of Array.from(root.querySelectorAll('*'))) {
    hardenElement(element);
    if (element.shadowRoot) hardenRoot(element.shadowRoot, listenedTargets);
    if (element instanceof HTMLIFrameElement) {
      try {
        if (element.contentDocument) {
          hardenRoot(element.contentDocument, listenedTargets);
        }
      } catch {
        // sandbox 或跨源 frame 不允许读取时，由其 sandbox 约束执行能力。
      }
    }
  }
}
export default function SecureFileViewer({
  url,
  name,
  size,
  className,
  canDownload = true,
  onDownload,
  onStateChange,
}: Props) {
  const boundaryRef = useRef<HTMLDivElement>(null);
  const [bundle, setBundle] = useState<FileViewerBundle | null>(null);
  const onStateChangeRef = useRef(onStateChange);
  onStateChangeRef.current = onStateChange;
  useEffect(() => {
    let active = true;
    loadFileViewer()
      .then((loaded) => {
        if (active) setBundle(loaded);
      })
      .catch((error) => {
        // 预览组件本身加载失败时沿用调用方的错误状态：父级会退回下载引导。
        if (active) onStateChangeRef.current?.({ error });
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    const boundary = boundaryRef.current;
    if (!boundary) return undefined;
    const listenedTargets = new WeakSet<EventTarget>();
    const harden = () => hardenRoot(boundary, listenedTargets);
    harden();
    const observer = new MutationObserver(harden);
    observer.observe(boundary, {
      childList: true,
      subtree: true,
    });
    // ShadowRoot 不在外层 MutationObserver 的 subtree 中，周期性发现异步创建
    // 的 ShadowRoot 和 iframe，并立即挂入同一套限制。
    const interval = window.setInterval(harden, 250);
    return () => {
      observer.disconnect();
      window.clearInterval(interval);
    };
  }, [url]);
  return (
    <div ref={boundaryRef} className={className}>
      {bundle ? (
        <bundle.FileViewer
          key={url}
          url={url}
          name={name}
          size={size}
          style={{
            width: '100%',
            height: '100%',
          }}
          onStateChange={(state) => onStateChange?.({ error: state.error })}
          options={{
            ...bundle.viewerOptions,
            theme: 'light',
            styleIsolation: 'shadow',
            toolbar: {
              position: 'bottom-right',
              print: false,
              exportHtml: false,
              permissions: {
                download: canDownload,
                print: false,
                'export-html': false,
              },
            },
            beforeOperation: (context) => {
              if (context.operation === 'download') {
                if (canDownload) onDownload?.();
                // 任何情况下都阻止预览器自带的下载，下载统一走我们的接口。
                return false;
              }
              return !['print', 'export-html'].includes(context.operation);
            },
          }}
        />
      ) : (
        <Spin
          style={{
            display: 'block',
            margin: 40,
          }}
        />
      )}
    </div>
  );
}
