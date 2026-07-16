import React, { Suspense, useEffect, useRef } from 'react';
import { Spin } from '@arco-design/web-react';
import uiText from '@/utils/uiText';
import { loadFileViewer } from '@/utils/filePreview';
const FileViewer = React.lazy(loadFileViewer);
interface Props {
  url: string;
  name: string;
  size?: number;
  className?: string;
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
    // 邮件 HTML 正文使用空 sandbox 的 srcdoc frame，无法从父页面可靠访问其
    // DOM；关闭该 frame 的指针事件，避免链接在子 frame 内发起导航。
    if (
      element.classList.contains('email-html') &&
      element.style.pointerEvents !== 'none'
    ) {
      element.style.setProperty('pointer-events', 'none', 'important');
    }
  }
  // 邮件附件会再次调用完整渲染器；当前组件没有按附件扩展名设置安全
  // 白名单的能力，因此禁用附件预览和其内置下载，避免绕过顶层格式检查。
  if (
    element instanceof HTMLButtonElement &&
    (element.classList.contains('attachment-item') ||
      element.closest('.attachment-preview-head'))
  ) {
    element.disabled = true;
    element.setAttribute('aria-disabled', 'true');
    element.title = uiText('邮件附件请下载原文件后查看');
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
  onDownload,
  onStateChange,
}: Props) {
  const boundaryRef = useRef<HTMLDivElement>(null);
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
    // 的 ShadowRoot 和 EPUB iframe，并立即挂入同一套限制。
    const interval = window.setInterval(harden, 250);
    return () => {
      observer.disconnect();
      window.clearInterval(interval);
    };
  }, [url]);
  return (
    <div ref={boundaryRef} className={className}>
      <Suspense
        fallback={
          <Spin
            style={{
              display: 'block',
              margin: 40,
            }}
          />
        }
      >
        <FileViewer
          key={url}
          url={url}
          name={name}
          size={size}
          style={{
            width: '100%',
            height: '100%',
          }}
          onStateChange={onStateChange}
          options={{
            theme: 'light',
            styleIsolation: 'shadow',
            archive: {
              entryActions: {
                download: false,
              },
              // 压缩包条目同样会进入嵌套渲染器。限制为 1 字节等同于关闭
              // 实际文件的内嵌预览，同时保留安全的目录浏览能力。
              maxEntryPreviewSize: 1,
            },
            toolbar: {
              position: 'bottom-right',
              print: false,
              exportHtml: false,
              permissions: {
                print: false,
                'export-html': false,
              },
            },
            beforeOperation: (context) => {
              if (context.operation === 'download') onDownload?.();
              return !['download', 'print', 'export-html'].includes(
                context.operation
              );
            },
          }}
        />
      </Suspense>
    </div>
  );
}
