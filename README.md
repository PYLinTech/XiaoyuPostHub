# XiaoyuPostHub

XiaoyuPostHub 是一个自建文件存储与分享系统，包含 Go 后端、React 前端和 PostgreSQL 数据库。

## 快速安装

中国大陆用户：

```bash
curl -fL 'https://raw.giteeusercontent.com/PYLinTech/XiaoyuPostHub/raw/main/install.sh' -o install.sh && chmod +x install.sh && ./install.sh
```

国际用户：

```bash
curl -fL 'https://raw.githubusercontent.com/PYLinTech/XiaoyuPostHub/refs/heads/main/install.sh' -o install.sh && chmod +x install.sh && ./install.sh
```

## 已实现功能

- 文件与文件夹管理：分片上传、断点续传、全平台 SHA-256 秒传、目录浏览、预览、重命名、用户隔离回收站和打包下载。
- 分享与直链：随机链接、可选密码和有效期、下载次数与流量限制、批量启停和重新配置。
- 账户与权限：注册、邀请码、用户组、组级权限和配额、用户禁用与密码重设。
- 管理功能：运行概览、全量文件与分享审核、系统审计、站点、下载策略和上传并发配置。
- 站内消息：面向全体、用户组或用户投递，支持已读和删除状态。

上传任务由数据库按用户隔离保存。前端提供可折叠的上传任务面板，支持暂停、继续、删除和调整队列顺序；刷新页面或意外中断后可恢复任务。系统配置可调整分片大小（默认 8M）、单任务分片并发数和单用户任务并发数，并可发送测试分片验证反向代理请求体限制。

用户删除的文件和文件夹默认进入各自的回收站，可逐项恢复、永久删除或清空。回收站保留期限由系统管理员配置，默认 30 天；到期内容由后台自动清理。

管理员审查页按上传任务分页展示全部用户文件，并保留任务内文件树的勾选联动；支持按 ID、文件名或用户名筛选、批量下载、通过或驳回、删除及拉黑。管理员删除的文件进入受限回收站，用户可见但不能恢复；撤销处置时会在文件仍存在的情况下自动还原。分享审查支持批量预览渲染内容或源码、删除及封禁链接。所有审核结果都会写入用户站内消息和系统审计日志。

审核持久化使用全新的 `file_moderations` 与 `share_moderations` 模型，不读取或迁移未投入使用的旧审核表；启动更新时会直接清理旧表。

## 目录

- `backend/`：Go API、数据库 schema 和业务逻辑。
- `frontend/`：React 用户界面。
- `deploy/`：Docker 镜像与 Compose 配置。

## 许可证

项目基于 [Apache License 2.0](LICENSE) 开源。文件预览组件的第三方声明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

Copyright © 2026 PYLinTech（重庆沛雨霖科技有限公司）
