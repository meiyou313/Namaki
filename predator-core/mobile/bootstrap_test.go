// SPDX-License-Identifier: GPL-3.0-or-later
package mobile

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/experimental/libbox"
)

const fixtureVLESS = "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.net:443?security=reality&sni=www.example.com&fp=chrome&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&type=grpc&serviceName=TunService#test"

type blockedGuardianTransport struct{ paths []string }

func (s *blockedGuardianTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.paths = append(s.paths, req.URL.Path)
	return &http.Response{StatusCode: 451, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{\"message\":\"Region unavailable\"}")), Request: req}, nil
}
func TestBootstrapSubscriptionParserRealityGRPC(t *testing.T) {
	input := strings.Join([]string{"# bundle", fixtureVLESS, fixtureVLESS, "vless://aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee@example.net:443?security=reality&pbk=key&type=xhttp#unsupported"}, string(rune(10)))
	nodes := parseBootstrapNodes(input)
	if len(nodes) != 1 {
		t.Fatalf("expected exactly one compatible node, got %d", len(nodes))
	}
	if nodes[0].outbound["type"] != "vless" {
		t.Fatal("missing vless outbound")
	}
	tls, ok := nodes[0].outbound["tls"].(object)
	if !ok {
		t.Fatal("missing TLS")
	}
	real, ok := tls["reality"].(object)
	if !ok || real["enabled"] != true {
		t.Fatal("missing Reality")
	}
	transport, ok := nodes[0].outbound["transport"].(object)
	if !ok || transport["type"] != "grpc" || transport["service_name"] != "TunService" {
		t.Fatal("gRPC not preserved")
	}
	if len(parseBootstrapNodes("xhttp://unsupported")) != 0 {
		t.Fatal("unsupported node accepted")
	}
	if err := libbox.CheckConfig(bootstrapConfig(nodes[0], 30805)); err != nil {
		t.Fatalf("sing-box rejects bootstrap config: %v", err)
	}
}
func TestBootstrapDoubleSlashScope(t *testing.T) {
	for _, tc := range []struct {
		u     string
		allow bool
	}{
		{"https://vpn.mozilla.org/api/v1/fpn/status", true},
		{"https://vpn.mozilla.org/api/v1/fpn/token", true},
		{"https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records", true},
		{"https://vpn.mozilla.org//api/v1/fpn/token", false},
		{"http://vpn.mozilla.org/api/v1/fpn/token", false},
		{"https://api.accounts.firefox.com/v1/oauth/token", false},
		{"https://other.example/api/v1/fpn/status", false},
	} {
		if guardianRecoveryURL(tc.u) != tc.allow {
			t.Fatalf("unexpected scope: %s", tc.u)
		}
		path, ok := doubleSlashGuardianURL(tc.u)
		should := tc.allow && strings.Contains(tc.u, "vpn.mozilla.org")
		if ok != should {
			t.Fatalf("wrong double slash eligibility: %s", tc.u)
		}
		if ok && !strings.Contains(path, "vpn.mozilla.org//api/v1/fpn/") {
			t.Fatalf("wrong URL: %s", path)
		}
	}
}
func TestGuardian451UsesDoubleSlashOnlyWhenSubscriptionsDisabled(t *testing.T) {
	b := NewBackend(&testHost{}, "{}")
	b.mu.Lock()
	b.settings["bootstrapSubUrls"] = []any{}
	b.mu.Unlock()
	trans := &blockedGuardianTransport{}
	b.client = &http.Client{Transport: trans}
	_, _, err := b.jsonRequest(context.Background(), "https://vpn.mozilla.org/api/v1/fpn/token", "GET", nil, "Bearer example")
	if err == nil || !strings.Contains(err.Error(), "Region unavailable") {
		t.Fatalf("wrong 451 recovery error: %v", err)
	}
	if len(trans.paths) != 2 || trans.paths[0] != "/api/v1/fpn/token" || trans.paths[1] != "//api/v1/fpn/token" {
		t.Fatalf("request path changed or unexpected retries: %v", trans.paths)
	}
}
