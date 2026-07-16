# XiaoyuPostHub

XiaoyuPostHub 是一个自建文件存储与分享系统，包含 Go 后端、React 前端和 PostgreSQL 数据库。

## 已实现功能

- 文件与文件夹管理：多文件上传、SHA-256 完整性校验、目录浏览、预览、重命名、删除和打包下载。
- 分享与直链：随机链接、可选密码和有效期、下载次数与流量限制、批量启停和重新配置。
- 账户与权限：注册、邀请码、用户组、组级权限和配额、用户禁用与密码重设。
- 管理功能：运行概览、文件与分享说明审核、系统审计、站点和下载策略配置。
- 站内消息：面向全体、用户组或用户投递，支持已读和删除状态。

项目暂未实现分片上传、断点续传、秒传、回收站、水印或组织架构功能。

## 目录

- `backend/`：Go API、数据库 schema 和业务逻辑。
- `frontend/`：React 用户界面。
- `deploy/`：Docker 镜像与 Compose 配置。

## 许可证

项目基于 [Apache License 2.0](LICENSE) 开源。文件预览组件的第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

Copyright © 2026 PYLinTech（重庆沛雨霖科技有限公司）
