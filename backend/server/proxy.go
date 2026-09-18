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

// SetTrustedProxies 设置可信反向代理网段：
//   - 传入空列表：保持历史行为——始终优先采信 X-Real-IP（适用于服务只经
//     反向代理暴露的部署）；
//   - 传入网段：仅当直连来源（RemoteAddr）命中网段时才采信 X-Real-IP，
//     防止可直连的客户端伪造该头绕过基于 IP 的登录限流。
func SetTrustedProxies(nets []*net.IPNet) {
	trustedProxyNets = nets
}

// clientIP 解析请求的真实客户端地址，用于登录限流与审计日志。
//
// 优先取反向代理写入的 X-Real-IP；未配置可信网段时无条件采信（历史行为），
// 配置后仅采信来自可信代理的请求。其余情况一律回落到传输层地址。
func clientIP(r *http.Request) string {
	remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteHost = r.RemoteAddr
	}
	remoteIP := net.ParseIP(remoteHost)

	if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); forwarded != nil {
		if len(trustedProxyNets) == 0 || ipInNets(remoteIP, trustedProxyNets) {
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
