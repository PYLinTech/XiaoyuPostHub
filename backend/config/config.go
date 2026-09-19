// Package config 负责加载与校验 XiaoyuPostHub 后端运行配置。
//
// 设计要点：
//
//   - 配置源有两层：deploy/.env 文件与进程环境变量。两者 key 完全一致，
//     采用 12-factor 风格：环境变量优先级高于 .env 文件，便于容器化部署时
//     通过 docker compose / -e 临时覆盖。
//   - .env 解析器自带实现，不引入第三方依赖。支持的语法：
//     · 空行 / 以 # 开头的整行注释
//     · 可选 `export ` 前缀
//     · KEY=VALUE，VALUE 两侧空白会被 trim
//     · VALUE 若以双引号包围会去引号，并支持 \\、\" 转义
//   - 校验失败立即返回 error，main.go 启动期直接 log.Fatalf。
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// Config 持有后端启动所需的全部配置。
//
// 字段对外暴露以便测试与 main.go 直接读取；
// 任何打印或日志都不应输出 Config / 包含其中的密码哈希。
//
// Config 承载后端进程运行需要的数据库、超管、静态目录和 Cookie 配置。
type Config struct {
	// DatabaseURL 是 PostgreSQL 连接字符串，
	// 形如：postgresql://user:password@host:5432/dbname?sslmode=disable。
	DatabaseURL string

	// SuperAdminUsername 是首次启动时初始化超级管理员账号的用户名。
	SuperAdminUsername string

	// SuperAdminPasswordHash 是超级管理员密码的存储哈希，
	// 必须是 bcrypt cost=12 完整字符串（$2a$12$...）。
	// 启动期 user.BootstrapSuperAdmin 会用 user.ValidatePasswordHash 强校验。
	SuperAdminPasswordHash string
	StaticDir              string

	// HTTPSEnabled 声明站点是否通过 HTTPS 对外提供服务，默认为 true。
	//
	// 它同时决定两件事：
	//   - 会话 Cookie 是否带 Secure 属性（纯 HTTP 部署必须为 false，否则
	//     浏览器不会回传 Cookie，登录状态无法保持）；
	//   - 登录加密使用的填充方式（HTTPS → RSA-OAEP，纯 HTTP → RSA1_5）。
	//
	// 算法由服务端固定下发，前端不按浏览器环境自动回退，避免攻击者把加密
	// 强度拉低到较弱的填充。
	HTTPSEnabled bool

	// HSTSEnabled 控制是否下发 Strict-Transport-Security 响应头，默认为 true。
	//
	// 响应头只在 HTTPS 上被浏览器采纳（HTTP 响应会被忽略），因此默认启用对
	// 明文部署没有副作用；纯 HTTP 部署如需完全静默可显式配置 false。
	HSTSEnabled bool

	// TrustedProxyCIDRs 是可选的可信反向代理网段（逗号分隔 CIDR，如
	// "172.17.0.0/16,127.0.0.1/32"）。
	//
	// 设置后，只有直连来源落在这些网段内才采信 X-Real-IP；留空时保持历史
	// 行为（始终优先采信 X-Real-IP）。当服务可能被直接访问（无代理）时，
	// 建议显式配置，避免客户端伪造 X-Real-IP 绕过基于 IP 的登录限流。
	TrustedProxyCIDRs []*net.IPNet

	// EncryptionKeys 是 KEK（密钥加密密钥）列表，格式：
	//   XPH_ENCRYPTION_KEYS=keyId:base64(32字节),keyId2:base64(32字节)
	//
	// 第一个条目用于加密新文件，其余条目用于解密历史数据（密钥轮换）。
	// 留空表示未配置：加密开关不可启用（上传会明确失败），明文功能不受影响。
	//
	// 安全约束（务必写入部署文档）：KEK 丢失等于已加密文件永久不可恢复；
	// 该值属于敏感配置，不写入数据库、不进入日志。
	EncryptionKeys string

	// Pan123ClientID / Pan123ClientSecret 是 123 云盘开放平台的应用凭据。
	//
	// 仅在启用 123 云盘存储后端时需要：客户端 ID 与密钥以站内信下发，属于敏感
	// 配置（不入库、不写日志）。存储根目录文件夹 ID 等非敏感参数配置在
	// storage_backends.settings 中。
	Pan123ClientID     string
	Pan123ClientSecret string

	// S3AccessKeyID / S3SecretAccessKey 是 S3 兼容对象存储的访问凭据。
	//
	// 仅在启用对象存储后端时需要，属于敏感配置（不入库、不写日志）。Endpoint /
	// Bucket / Region / 前缀等非敏感参数配置在 storage_backends.settings 中。
	S3AccessKeyID     string
	S3SecretAccessKey string

	// HostDiskPath 是管理端「实时概览」统计的宿主磁盘路径（默认 "/"）。
	//
	// 概览展示的是"这台机器"的磁盘，因此不跟随管理端可改的存储路径，也不受远端
	// 存储后端影响（远端容量不属于宿主磁盘口径）；做成部署级配置，便于数据盘挂在
	// 其它路径时指过去。读取失败只会让该卡片显示"不可用"，不影响概览其它数据。
	HostDiskPath string

	// EnvFile 是实际加载的 .env 路径，可能为空（表示完全依赖环境变量）。
	EnvFile string
}

// Load 按以下优先级解析配置并强校验：
//
//  1. 读取 path 指定的 .env 文件（允许为空字符串：跳过文件读取）
//  2. 用进程环境变量覆盖同名 key
//  3. 校验必填字段
//
// envFile 不存在会被视为非致命错误（视为缺少源）；解析失败则返回 error。
// 字段缺失或为空返回 *ValidationError 包装的错误。
func Load(envFile string) (*Config, error) {
	fileKeys, err := readEnvFile(envFile)
	if err != nil {
		return nil, fmt.Errorf("读取 .env 失败：%w", err)
	}

	c := &Config{
		DatabaseURL:            pickValue("DATABASE_URL", fileKeys),
		SuperAdminUsername:     pickValue("SUPER_ADMIN_USERNAME", fileKeys),
		SuperAdminPasswordHash: pickValue("SUPER_ADMIN_PASSWORD_HASH", fileKeys),
		StaticDir:              pickValue("STATIC_DIR", fileKeys),
		EncryptionKeys:         pickValue("XPH_ENCRYPTION_KEYS", fileKeys),
		Pan123ClientID:         pickValue("XPH_PAN123_CLIENT_ID", fileKeys),
		Pan123ClientSecret:     pickValue("XPH_PAN123_CLIENT_SECRET", fileKeys),
		S3AccessKeyID:          pickValue("XPH_S3_ACCESS_KEY_ID", fileKeys),
		S3SecretAccessKey:      pickValue("XPH_S3_SECRET_ACCESS_KEY", fileKeys),
		HostDiskPath:           pickValue("XPH_HOST_DISK_PATH", fileKeys),
		HTTPSEnabled:           true,
		HSTSEnabled:            true,
		EnvFile:                envFile,
	}
	if strings.TrimSpace(c.StaticDir) == "" {
		c.StaticDir = "/app/web"
	}
	if strings.TrimSpace(c.HostDiskPath) == "" {
		c.HostDiskPath = "/"
	}
	if raw, ok := pickOptionalValue("HTTPS_ENABLED", fileKeys); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			c.HTTPSEnabled = true
		case "false":
			c.HTTPSEnabled = false
		default:
			return nil, fmt.Errorf("HTTPS_ENABLED 只能是 true 或 false")
		}
	}
	if raw, ok := pickOptionalValue("HSTS_ENABLED", fileKeys); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true":
			c.HSTSEnabled = true
		case "false":
			c.HSTSEnabled = false
		default:
			return nil, fmt.Errorf("HSTS_ENABLED 只能是 true 或 false")
		}
	}
	if raw, ok := pickOptionalValue("TRUSTED_PROXY_CIDRS", fileKeys); ok {
		nets, err := parseCIDRList(raw)
		if err != nil {
			return nil, err
		}
		c.TrustedProxyCIDRs = nets
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// validate 校验必填字段。注意是 *ValidationError，main.go 可用 errors.As 判定类型。
func (c *Config) validate() error {
	var missing []string
	if strings.TrimSpace(c.DatabaseURL) == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if strings.TrimSpace(c.SuperAdminUsername) == "" {
		missing = append(missing, "SUPER_ADMIN_USERNAME")
	}
	if strings.TrimSpace(c.SuperAdminPasswordHash) == "" {
		missing = append(missing, "SUPER_ADMIN_PASSWORD_HASH")
	}
	if len(missing) == 0 {
		return nil
	}
	return &ValidationError{Missing: missing}
}

// ValidationError 表示必填字段缺失。
type ValidationError struct {
	Missing []string
}

func (e *ValidationError) Error() string {
	return "配置缺少必填字段：" + strings.Join(e.Missing, ", ")
}

// parseCIDRList 解析逗号分隔的 CIDR 列表（同时支持 IPv4 与 IPv6 网段）。
// 空白项会被忽略；任一项非法立即返回错误，避免静默降级为“不信任任何代理”。
func parseCIDRList(raw string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, block, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS 含无效网段 %q：%w", part, err)
		}
		out = append(out, block)
	}
	return out, nil
}

// pickValue 先看环境变量，再退回 .env。空字符串才算未设置。
func pickValue(key string, file map[string]string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return file[key]
}

func pickOptionalValue(key string, file map[string]string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	v, ok := file[key]
	return v, ok
}

// readEnvFile 逐行解析 .env 文件。
//
// 返回值只包含文件里出现的 key；返回的 map 可由调用方与 os.Getenv 合并。
// 不存在的文件不报错（视为空文件），便于"未配置 .env"场景。
func readEnvFile(path string) (map[string]string, error) {
	out := make(map[string]string)
	if path == "" {
		return out, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()

	return parseEnvReader(f, out)
}

// parseEnvReader 是核心解析器，从任意 io.Reader 读入 .env 内容。
// 拆成独立函数便于在白盒测试里直接喂字符串。
func parseEnvReader(r io.Reader, out map[string]string) (map[string]string, error) {
	sc := bufio.NewScanner(r)
	// 默认 64KB 缓冲足够，单行 .env 不会超过；极端情况由 maxLineBytes 自动扩容。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		key, value, ok := parseEnvLine(raw)
		if !ok {
			continue // 空行 / 注释 / 跳过
		}
		out[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("第 %d 行附近：%w", lineNo, err)
	}
	return out, nil
}

// parseEnvLine 解析单行 .env；返回 ok=false 表示该行可忽略（注释/空行）。
//
// 支持的语法：
//   - 行首允许 "export "
//   - 行内注释 "# foo" 不支持（避免误杀 URL 里的 #）
//   - 右侧空白会被 trim
//   - VALUE 支持单引号字面量和双引号转义值
func parseEnvLine(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")

	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 {
		// 没有 = 或空 key，全部当注释行跳过（容错优先于严格校验）。
		return "", "", false
	}
	key = strings.TrimSpace(trimmed[:eq])
	if key == "" {
		return "", "", false
	}
	value = unquote(strings.TrimSpace(trimmed[eq+1:]))
	return key, value, true
}

// unquote 处理单引号字面量和双引号转义值。输入已 trim 两侧空白。
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	const quote = '"'
	if len(s) >= 2 && s[0] == quote && s[len(s)-1] == quote {
		body := s[1 : len(s)-1]
		// 仅展开两个最常见的转义；其它反斜杠保持原样。
		body = strings.ReplaceAll(body, `\\`, "\x00")
		body = strings.ReplaceAll(body, `\"`, `"`)
		body = strings.ReplaceAll(body, "\x00", `\`)
		return body
	}
	return s
}
