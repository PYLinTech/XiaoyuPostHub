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

module.exports = {
  webpack: override(
    (config) => {
      config.resolve.fallback = {
        ...(config.resolve.fallback || {}),
        assert: require.resolve('assert/'),
        buffer: require.resolve('buffer/'),
        fs: false,
        'fs/promises': false,
        path: require.resolve('path-browserify'),
        process: require.resolve('process/browser'),
        stream: require.resolve('stream-browserify'),
        util: require.resolve('util/'),
        zlib: require.resolve('browserify-zlib'),
      };
      config.ignoreWarnings = [
        ...(config.ignoreWarnings || []),
        {
          module: /diff2html/,
          message: /Failed to parse source map/,
        },
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
