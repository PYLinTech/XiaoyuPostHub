package db

import "embed"

// embeddedSchemaFS 把 backend/db/schema/*.sql 编译进后端二进制，
// 让运行时启动期不再依赖磁盘上的 schema 目录。
//
// SQL 文件仍然保留在 backend/db/schema/，原因是：
//   - sqlc 还需要它生成 db/generated/*.go（构建期用途）
//   - 编辑器 / sqlc 工具链的可读性
//
// 运行时唯一的"启动期 schema 来源"是这个 embed.FS。
//
//go:embed schema/*.sql
var embeddedSchemaFS embed.FS
