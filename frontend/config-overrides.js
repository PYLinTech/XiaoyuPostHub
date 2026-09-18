/* eslint-disable @typescript-eslint/no-var-requires */
const path = require('path');
const {
  override,
  addWebpackModuleRule,
  addWebpackPlugin,
  addWebpackAlias,
} = require('customize-cra');
const ArcoWebpackPlugin = require('@arco-plugins/webpack-react');
const addLessLoader = require('customize-cra-less-loader');

// @prettier/plugin-xml 只在 XML/HTML 格式化时按需加载，并引用 prettier 3 的
// `prettier/doc` 子路径；但该插件被提升到顶层，而顶层 prettier 是开发工具链
// 使用的 2.x（没有该子路径）。这里从 renderer-text 的依赖位置解析 prettier 3
// 的 doc 入口，保证按需能力在构建期可解析。
function resolvePrettierDocEntry() {
  try {
    const rendererTextDir = path.dirname(
      require.resolve('@file-viewer/renderer-text/package.json')
    );
    const prettierDir = path.dirname(
      require.resolve('prettier/package.json', { paths: [rendererTextDir] })
    );
    return path.join(prettierDir, 'doc.mjs');
  } catch {
    return undefined;
  }
}

const prettierDocEntry = resolvePrettierDocEntry();

module.exports = {
  webpack: override(
    (config) => {
      // 生产构建不输出 source map：发布产物直接进入镜像，体积优先；
      // 需要定位线上问题时用同版本源码在本地构建。
      if (config.mode === 'production') {
        config.devtool = false;
      }
      // 保留的图片/文本/办公文档/PDF 渲染器不依赖 Node 内置模块，仅需屏蔽
      // 未使用的 fs 引用。
      config.resolve.fallback = {
        ...(config.resolve.fallback || {}),
        fs: false,
        'fs/promises': false,
      };
      // 生产构建已关闭 devtool，第三方包内残留的 sourceMappingURL 引用无法解析，
      // 属于无意义警告。
      config.ignoreWarnings = [
        ...(config.ignoreWarnings || []),
        { message: /Failed to parse source map/ },
      ];
      return config;
    },
    addLessLoader({
      lessLoaderOptions: {
        // Arco 2.x 仍使用 Less 的 @plugin 语法；只隐藏该上游弃用噪音。
        lessOptions: { quietDeprecations: true },
      },
    }),
    // File Viewer 使用严格 ESM，部分按需 renderer 仍保留无扩展名的内部导入。
    // Webpack 5 默认要求 fully specified；仅对该依赖放宽解析以兼容 CRA 5。
    addWebpackModuleRule({
      test: /\.m?js$/,
      include: /node_modules\/@file-viewer/,
      resolve: { fullySpecified: false },
    }),
    // 基础样式由入口统一加载，避免异步页面各自注入组件样式造成 CSS 顺序冲突。
    addWebpackPlugin(new ArcoWebpackPlugin({ style: false })),
    addWebpackAlias({
      '@': path.resolve(__dirname, 'src'),
      ...(prettierDocEntry ? { 'prettier/doc': prettierDocEntry } : {}),
    })
  ),
  devServer: (configFunction) => (proxy, allowedHost) => {
    const config = configFunction(proxy, allowedHost);
    const overlay =
      config.client && typeof config.client.overlay === 'object'
        ? config.client.overlay
        : {};

    config.client = config.client || {};
    config.client.overlay = {
      ...overlay,
      errors: true,
      warnings: false,
      runtimeErrors: (error) => {
        const message = error && error.message ? error.message : String(error);
        return !(
          message.includes('ResizeObserver loop limit exceeded') ||
          message.includes(
            'ResizeObserver loop completed with undelivered notifications'
          )
        );
      },
    };

    return config;
  },
};
