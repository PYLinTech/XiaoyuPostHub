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
	"os"
	"strings"
)

// Config 持有后端启动所需的全部配置。
//
// 字段对外暴露以便测试与 main.go 直接读取；
// 任何打印或日志都不应输出 Config / 包含其中的密码哈希。
type Config struct {
	// DatabaseURL 是 PostgreSQL 连接字符串，
	// 形如：postgresql://user:password@host:5432/dbname?sslmode=disable。
	DatabaseURL string

	// SuperAdminUsername 是首次启动时初始化超级管理员账号的用户名。
	SuperAdminUsername string

	// SuperAdminPasswordHash 是超级管理员密码的存储哈希，
	// 推荐格式：sha256:<salt>:<hash>。
	SuperAdminPasswordHash string

	// StaticDir 是前端构建产物所在目录；留空时由 main.go 用解析可执行文件
	// 同级目录的 web/ 作为默认值，便于单二进制内置静态资源。
	StaticDir string

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
		StaticDir:              pickValue("XPH_STATIC_DIR", fileKeys),
		EnvFile:                envFile,
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

// pickValue 先看环境变量，再退回 .env。空字符串才算未设置。
func pickValue(key string, file map[string]string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return file[key]
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
//   - VALUE 若首尾都是 " 则去引号，"\\" 与 "\"" 转义被还原
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

// unquote 处理双引号包围 / 转义。输入已 trim 两侧空白。
func unquote(s string) string {
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
