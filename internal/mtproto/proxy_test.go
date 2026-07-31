package mtproto

import (
	"context"
	"testing"
	"time"
)

func TestParseProxyURLRequiresSchemeAndHost(t *testing.T) {
	parsed, err := parseProxyURL("HTTP_PROXY", "http://user:pass@proxy.local:8080")
	if err != nil {
		t.Fatalf("parse proxy: %v", err)
	}
	if parsed.Scheme != "http" || parsed.Host != "proxy.local:8080" || parsed.User.Username() != "user" {
		t.Fatalf("unexpected proxy URL: %s", parsed.String())
	}
	if _, err := parseProxyURL("HTTP_PROXY", "proxy.local:8080"); err == nil {
		t.Fatalf("expected missing scheme error")
	}
}

func TestResolveProxyURLHonorsPriorityAndNoProxy(t *testing.T) {
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("HTTPS_PROXY", "http://https-proxy.local:8080")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTP_PROXY", "http://http-proxy.local:8080")
	t.Setenv("http_proxy", "")
	t.Setenv("NO_PROXY", "example.com,.internal,10.0.0.0/8")
	t.Setenv("no_proxy", "")

	if got, err := resolveProxyURL("api.telegram.org:443"); err != nil || got.String() != "http://https-proxy.local:8080" {
		t.Fatalf("proxy=%v err=%v", got, err)
	}
	if got, err := resolveProxyURL("service.internal:443"); err != nil || got != nil {
		t.Fatalf("expected no proxy for internal domain, got %v err=%v", got, err)
	}
	if got, err := resolveProxyURL("10.1.2.3:443"); err != nil || got != nil {
		t.Fatalf("expected no proxy for CIDR, got %v err=%v", got, err)
	}
}

func TestShouldBypassProxySupportsWildcardDomainsAndIPs(t *testing.T) {
	t.Setenv("NO_PROXY", "localhost,.example.org,192.168.1.10")
	t.Setenv("no_proxy", "")
	if !shouldBypassProxy("localhost") {
		t.Fatalf("expected localhost bypass")
	}
	if !shouldBypassProxy("api.example.org") {
		t.Fatalf("expected subdomain bypass")
	}
	if !shouldBypassProxy("192.168.1.10") {
		t.Fatalf("expected IP bypass")
	}
	if shouldBypassProxy("telegram.org") {
		t.Fatalf("did not expect unrelated host bypass")
	}
}

func TestCtxDeadline(t *testing.T) {
	//lint:ignore SA1012 ctxDeadline explicitly supports a nil context as no deadline.
	if _, ok := ctxDeadline(nil); ok {
		t.Fatalf("nil context should not have deadline")
	}
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	got, ok := ctxDeadline(ctx)
	if !ok || !got.Equal(deadline) {
		t.Fatalf("deadline=%s ok=%t", got, ok)
	}
}
