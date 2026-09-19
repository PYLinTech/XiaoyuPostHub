-- 039.sql — 补上 037 遗漏的 system_settings.redirect_fallback 列。
--
-- 037.sql 的头注释声明「新增 redirect_fallback」，但正文只做了删列与改默认值，
-- 漏写了 ALTER TABLE ... ADD COLUMN，导致该列在数据库里根本不存在；而
-- systemsetting.Knobs 的原始 SQL（GetKnobs / UpdateKnobs）会读写它，凡是走到
-- Knobs 的请求都会报 "column \"redirect_fallback\" does not exist"：
--   * GET /api/admin/system-config → 500，管理端「系统配置」页提示「系统配置加载失败」；
--   * PUT /api/admin/system-config → 500，保存系统配置失败；
--   * 站点配置、分享创建、下载策略等读取该开关的接口一并 500。
--
-- 这里按 037 声明的语义把列补上（TRUE = 302 取数不可用时自动降级本机中转）。
-- 该列由 systemsetting.Knobs 用原始 SQL 读写，不依赖 sqlc 生成代码。
--
-- 兼容性：只增列，旧版本二进制不读取该列，可安全回滚。

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS redirect_fallback BOOLEAN NOT NULL DEFAULT TRUE;

COMMENT ON COLUMN system_settings.redirect_fallback IS
    '302 取数（直链）不可用时是否自动降级本机中转；默认开启。';
