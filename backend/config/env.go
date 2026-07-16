package config

// 启动期从 .env 加载的"运行时身份"全局变量。
// 不属于 Config struct(那是配置),这里是运行期状态。
//
// 由 main.go 在 config.Load 之后赋值,运行时只读。
var (
	EnvSuperAdmin             string
	EnvSuperAdminPasswordHash string
)
