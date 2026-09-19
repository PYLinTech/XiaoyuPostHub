package server

import (
	"net"
	"net/http"
	"strings"
)

// trustedProxyNets 是可选的可信反向代理网段，由 main 在启动时通过
// SetTrustedProxies 注入一次（与 config.EnvSuperAdmin 相同的装配模式），
// 避免把代理配置透传到每个 handler。
var trustedProxyNets []*net.IPNet

// SetTrustedProxies 设置可信反向代理网段：仅当直连来源（RemoteAddr）命中网段
// 时才采信 X-Real-IP。未配置时不再采信该头——可直连的客户端此前能伪造
// X-Real-IP 绕过基于 IP 的登录/取件码限流。反向代理部署请在 .env 配置
// TRUSTED_PROXY_CIDRS。
func SetTrustedProxies(nets []*net.IPNet) {
	trustedProxyNets = nets
}

// clientIP 解析请求的真实客户端地址，用于限流与审计日志。
//
// 仅当来源命中可信代理网段时才采用 X-Real-IP；其余情况一律使用传输层地址
// （RemoteAddr），避免伪造头绕过 IP 维度限流。
func clientIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)

	if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
		if len(trustedProxyNets) > 0 && ipInNets(remoteIP, trustedProxyNets) {
			return forwarded.String()
		}
	}
	if remoteIP != nil {
		return remoteIP.String()
	}
	return remoteHost
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, block := range nets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}
