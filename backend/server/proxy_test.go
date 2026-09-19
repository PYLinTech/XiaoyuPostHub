package server

import (
	"net"
	"net/http"
	"testing"
)

func newRequest(remoteAddr, forwarded string) *http.Request {
	r := &http.Request{RemoteAddr: remoteAddr, Header: http.Header{}}
	if forwarded != "" {
		r.Header.Set("X-Real-IP", forwarded)
	}
	return r
}

// 未配置可信网段时不再采信 X-Real-IP（防可直连客户端伪造头绕过 IP 维度限流）。
func TestClientIPIgnoresForwardedHeaderByDefault(t *testing.T) {
	SetTrustedProxies(nil)
	t.Cleanup(func() { SetTrustedProxies(nil) })

	if got := clientIP(newRequest("10.1.2.3:4567", "203.0.113.9")); got != "10.1.2.3" {
		t.Fatalf("未配置可信网段时不应采信 X-Real-IP，得到 %q", got)
	}
}

// 配置可信网段后，非可信来源伪造的 X-Real-IP 必须被忽略。
func TestClientIPRejectsForwardedHeaderFromUntrustedSource(t *testing.T) {
	_, block, err := net.ParseCIDR("172.17.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	SetTrustedProxies([]*net.IPNet{block})
	t.Cleanup(func() { SetTrustedProxies(nil) })

	if got := clientIP(newRequest("198.51.100.7:4567", "203.0.113.9")); got != "198.51.100.7" {
		t.Fatalf("非可信来源应忽略 X-Real-IP，得到 %q", got)
	}
}

// 来自可信代理网段的请求仍然采信 X-Real-IP。
func TestClientIPAcceptsForwardedHeaderFromTrustedProxy(t *testing.T) {
	_, block, err := net.ParseCIDR("172.17.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	SetTrustedProxies([]*net.IPNet{block})
	t.Cleanup(func() { SetTrustedProxies(nil) })

	if got := clientIP(newRequest("172.17.0.5:4567", "203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("可信代理应采信 X-Real-IP，得到 %q", got)
	}
}

// 没有 X-Real-IP（或值非法）时回落到传输层地址。
func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	SetTrustedProxies(nil)
	t.Cleanup(func() { SetTrustedProxies(nil) })

	if got := clientIP(newRequest("198.51.100.7:4567", "")); got != "198.51.100.7" {
		t.Fatalf("无 X-Real-IP 时应回落到 RemoteAddr，得到 %q", got)
	}
	if got := clientIP(newRequest("198.51.100.7", "")); got != "198.51.100.7" {
		t.Fatalf("RemoteAddr 无端口时应原样返回，得到 %q", got)
	}
	if got := clientIP(newRequest("198.51.100.7:4567", "not-an-ip")); got != "198.51.100.7" {
		t.Fatalf("非法 X-Real-IP 应被忽略，得到 %q", got)
	}
}
