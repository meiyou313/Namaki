// SPDX-License-Identifier: GPL-3.0-or-later
// Android's short-lived, control-plane-only HTTP-451 recovery.
// Double-leading-slash retry is confined to the Guardian API.
package mobile

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/experimental/libbox"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
)

const bootstrapNodeLimit = 40
const bootstrapNodesPerSource = 8
const bootstrapOnlineNodeBudget = 32
const bootstrapSourceLimit = 24

type bootstrapNode struct {
	outbound object
	raw      string
}

func bootstrapOutbound(raw string) (bootstrapNode, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "vless://") {
		return bootstrapNode{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User == nil || u.User.Username() == "" {
		return bootstrapNode{}, false
	}
	p := 443
	if u.Port() != "" {
		n, e := strconv.Atoi(u.Port())
		if e != nil || n <= 0 || n > 65535 {
			return bootstrapNode{}, false
		}
		p = n
	}
	q := u.Query()
	tr := strings.ToLower(q.Get("type"))
	if tr == "" {
		tr = strings.ToLower(q.Get("net"))
	}
	// Only transports supported by sing-box 1.12.0; XHTTP must never be rewritten.
	if tr != "grpc" && tr != "tcp" && tr != "ws" && tr != "" {
		return bootstrapNode{}, false
	}
	out := object{"type": "vless", "tag": "bootstrap", "server": u.Hostname(), "server_port": p, "uuid": u.User.Username()}
	if flow := q.Get("flow"); flow != "" {
		out["flow"] = flow
	}
	sec := strings.ToLower(q.Get("security"))
	if sec != "reality" && sec != "tls" && sec != "" {
		return bootstrapNode{}, false
	}
	if sec == "reality" || sec == "tls" || q.Get("sni") != "" {
		name := q.Get("sni")
		if name == "" {
			name = u.Hostname()
		}
		tlsCfg := object{"enabled": true, "server_name": name}
		// Do not silently disable certificate verification on user-supplied nodes.
		fp := q.Get("fp")
		if fp == "" && sec == "reality" {
			fp = "chrome"
		}
		if fp != "" {
			tlsCfg["utls"] = object{"enabled": true, "fingerprint": fp}
		}
		if sec == "reality" {
			pk := q.Get("pbk")
			if pk == "" {
				pk = q.Get("publickey")
			}
			if pk == "" {
				return bootstrapNode{}, false
			}
			reality := object{"enabled": true, "public_key": pk}
			if sid := q.Get("sid"); sid != "" {
				reality["short_id"] = sid
			}
			tlsCfg["reality"] = reality
		}
		out["tls"] = tlsCfg
	}
	switch tr {
	case "grpc":
		serviceName := q.Get("serviceName")
		if serviceName == "" {
			serviceName = q.Get("service_name")
		}
		if serviceName == "" {
			serviceName = "TunService"
		}
		out["transport"] = object{"type": "grpc", "service_name": serviceName}
	case "ws":
		path := q.Get("path")
		if path == "" {
			path = "/"
		}
		transport := object{"type": "ws", "path": path}
		if host := q.Get("host"); host != "" {
			transport["headers"] = object{"Host": host}
		}
		out["transport"] = transport
	}
	return bootstrapNode{outbound: out, raw: raw}, true
}

func parseBootstrapNodes(body string) []bootstrapNode {
	body = strings.TrimSpace(body)
	if body != "" && !strings.Contains(body, "://") {
		compact := strings.Join(strings.Fields(body), "")
		if value, err := base64.StdEncoding.DecodeString(compact); err == nil {
			body = string(value)
		}
		if value, err := base64.RawStdEncoding.DecodeString(compact); err == nil {
			body = string(value)
		}
	}
	seen := make(map[string]bool)
	result := make([]bootstrapNode, 0, 24)
	for _, line := range strings.Split(body, "\n") {
		node, ok := bootstrapOutbound(strings.TrimSpace(line))
		if !ok || seen[node.raw] {
			continue
		}
		seen[node.raw] = true
		result = append(result, node)
	}
	return result
}

func bootstrapConfig(node bootstrapNode, port int) string {
	config := object{
		"log":       object{"level": "error", "disabled": true},
		"inbounds":  []any{object{"type": "socks", "tag": "local", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{node.outbound, object{"type": "direct", "tag": "direct"}},
		"route":     object{"final": "bootstrap"},
	}
	data, _ := json.Marshal(config)
	return string(data)
}

// Keep private subscription contents and node authentication out of diagnostics.
func (b *Backend) bootstrapSources() []string {
	b.mu.Lock()
	settings := copyObject(b.settings)
	b.mu.Unlock()
	if !yes(settings, "subscriptionsEnabled") {
		return nil
	}
	sources := make([]string, 0)
	seen := make(map[string]bool)
	for _, item := range list(settings, "bootstrapSubUrls") {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		u, err := url.Parse(text)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || seen[text] {
			continue
		}
		seen[text] = true
		sources = append(sources, text)
		if len(sources) >= bootstrapSourceLimit {
			break
		}
	}
	return sources
}

func (b *Backend) fetchBootstrapNodes(ctx context.Context, address string) ([]bootstrapNode, error) {
	c, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PredatorVPN-Android/2.0.3")
	// The ordinary Android client keeps sockets outside our own TUN.
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, errors.New("subscription too large")
	}
	return parseBootstrapNodes(string(data)), nil
}

func freeBootstrapPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

func startBootstrapBox(node bootstrapNode, port int) (*box.Box, error) {
	ctx := libbox.BaseContext(nil)
	options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, []byte(bootstrapConfig(node, port)))
	if err != nil {
		return nil, err
	}
	engine, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return nil, err
	}
	if err = engine.Start(); err != nil {
		engine.Close()
		return nil, err
	}
	return engine, nil
}

// Arman compatibility workaround: two slashes immediately after vpn.mozilla.org.
// Do not rewrite arbitrary URLs, and never modify FxA token/authentication routes.
func doubleSlashGuardianURL(address string) (string, bool) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "vpn.mozilla.org") ||
		u.Port() != "" || u.User != nil || !strings.HasPrefix(u.Path, "/api/v1/fpn/") || strings.HasPrefix(u.Path, "//") {
		return "", false
	}
	u.Path = "/" + u.Path
	u.RawPath = ""
	return u.String(), true
}
func guardianRecoveryURL(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" &&
		(strings.EqualFold(u.Hostname(), "vpn.mozilla.org") || strings.EqualFold(u.Hostname(), "firefox.settings.services.mozilla.com")) &&
		u.Port() == "" && !strings.HasPrefix(u.Path, "//") &&
		(strings.HasPrefix(u.Path, "/api/v1/fpn/") ||
			(strings.EqualFold(u.Hostname(), "firefox.settings.services.mozilla.com") &&
				u.Path == "/v1/buckets/main/collections/vpn-serverlist/records"))
}

func (b *Backend) bootstrapClient(port int) *http.Client {
	proxyAddress := &url.URL{Scheme: "socks5", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyAddress), TLSClientConfig: &tls.Config{RootCAs: b.roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Timeout: 10 * time.Second, Jar: b.client.Jar, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// retryGuardian451 is invoked solely when the first response was HTTP 451.
// It never modifies the normal tunnel, app proxy settings, or the shared client.
func (b *Backend) retryGuardian451(ctx context.Context, address, method string, body []byte, headers http.Header) ([]byte, http.Header, int, error) {
	b.bootstrapMu.Lock()
	defer b.bootstrapMu.Unlock()
	sources := b.bootstrapSources()
	tried := make(map[string]bool)
	count := 0
	var last error
	probe := func(node bootstrapNode) ([]byte, http.Header, int, bool) {
		if tried[node.raw] || count >= bootstrapNodeLimit {
			return nil, nil, 0, false
		}
		tried[node.raw] = true
		count++
		port, err := freeBootstrapPort()
		if err != nil {
			last = err
			return nil, nil, 0, false
		}
		engine, err := startBootstrapBox(node, port)
		if err != nil {
			last = err
			return nil, nil, 0, false
		}
		defer engine.Close()
		client := b.bootstrapClient(port)
		defer client.CloseIdleConnections()
		data, h, status, err := b.fetchUsing(ctx, client, address, method, body, headers)
		if err != nil {
			last = err
			return nil, nil, 0, false
		}
		if status == 451 {
			last = errors.New("region restriction remains through this node")
			return nil, nil, 0, false
		}
		// Any non-451 HTTP status should go through normal JSON/401/403 handling.
		return data, h, status, true
	}
	for _, source := range sources {
		nodes, err := b.fetchBootstrapNodes(ctx, source)
		if err != nil {
			last = err
			continue
		}
		sourceTried := 0
		for _, node := range nodes {
			if tried[node.raw] {
				continue
			}
			if sourceTried >= bootstrapNodesPerSource || count >= bootstrapOnlineNodeBudget {
				break
			}
			sourceTried++
			if data, h, status, ok := probe(node); ok {
				return data, h, status, nil
			}
		}
		if count >= bootstrapOnlineNodeBudget {
			break
		}
	}
	// The preinstalled offline list still works when GitHub is inaccessible.
	if count < bootstrapNodeLimit {
		sourceTried := 0
		for _, node := range parseBootstrapNodes(b.host.BootstrapNodes()) {
			if tried[node.raw] {
				continue
			}
			if sourceTried >= bootstrapNodesPerSource || count >= bootstrapNodeLimit {
				break
			}
			sourceTried++
			if data, h, status, ok := probe(node); ok {
				return data, h, status, nil
			}
		}
	}
	// Expose a useful failure without leaking credentials, link contents or tokens.
	if last != nil {
		return nil, nil, 451, errors.New("Guardian is region-blocked and no temporary subscription connection succeeded")
	}
	return nil, nil, 451, errors.New("Guardian is region-blocked; no compatible temporary subscription nodes were found")
}
