// SPDX-License-Identifier: GPL-3.0-or-later
package mobile

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestChallengeJSONArguments(t *testing.T) {
	script := `init([],"old","/_fs-ch-test"); init ( [{"ty":"clientmetrics","data":{"items":[[1,2]],"text":"brackets ] and escaped \" quotes"}}], "token", "/_fs-ch-test", true);`
	tasks, token, err := parseChallengeInit(script, "/_fs-ch-test")
	if err != nil || len(tasks) != 1 || token != "token" {
		t.Fatalf("nested challenge rejected: %v", err)
	}
	for _, invalid := range []string{`init([],"t","/_fs-ch-test")`, `init([{"ty":"pow"}],"t","/other")`, `init([runUntrustedCode()],"t","/_fs-ch-test")`, `init([{"ty":"pow"}],"t","/_fs-ch-test" + runUntrustedCode())`} {
		if _, _, err := parseChallengeInit(invalid, "/_fs-ch-test"); err == nil {
			t.Fatal("invalid challenge accepted")
		}
	}
}

func TestAccountChallengeKeepsUserAgentAndFreshCookies(t *testing.T) {
	b := NewBackend(&testHost{}, "{}")
	site, _ := url.Parse("https://accounts.firefox.com/")
	b.client.Jar.SetCookies(site, []*http.Cookie{{Name: "stale", Value: "clearance", Path: "/"}})
	var loginAgent string
	solved := false
	b.client.Transport = exitRoundTripper(func(r *http.Request) (*http.Response, error) {
		if _, err := r.Cookie("stale"); err == nil {
			t.Fatal("Stale challenge cookies reached the fresh solver")
		}
		response := &http.Response{StatusCode: 200, Header: make(http.Header), Request: r}
		body := "{}"
		switch {
		case r.URL.Hostname() == "api.accounts.firefox.com":
			if r.Method == "POST" {
				loginAgent = r.UserAgent()
				cookie, err := r.Cookie("clearance")
				if err != nil || cookie.Value != "accepted" {
					t.Fatal("verification cookie not installed on API host")
				}
				body = `{"ok":true}`
			}
		case r.URL.Path == "/":
			// Once the post-back is accepted the server stops
			// challenging: the re-fetch after a success must see a
			// plain page, not the challenge again.
			if solved {
				body = `<title>Firefox Accounts</title>`
			} else {
				body = `<title>Client Challenge</title>/_fs-ch-test`
			}
		case strings.HasSuffix(r.URL.Path, "script.js"):
			body = `init ([{"ty":"clientmetrics","data":{"nested":[[1,2]]}}], "token", "/_fs-ch-test", true)`
		case strings.HasSuffix(r.URL.Path, "fst-post-back"):
			body = `{"status":"success"}`
			response.Header.Set("Set-Cookie", "clearance=accepted; Domain=firefox.com; Path=/; HttpOnly")
			solved = true
		default:
			t.Fatal("Unexpected verification request")
		}
		response.Body = io.NopCloser(strings.NewReader(body))
		return response, nil
	})
	// The account hosts travel the sign-in client now: the mock
	// answers on both, or the solver's fetches would leave the test
	// for the real network.
	b.signinClient.Transport = b.client.Transport
	if err := b.challenge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := b.fetch(context.Background(), "https://api.accounts.firefox.com/v1/account/login", "POST", []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	// The solver may try the browser and desktop agents while probing
	// (the way FoxyVPN does), but the account's own request keeps the
	// account's agent.
	if loginAgent != controlPlaneAgent {
		t.Fatalf("Account user agent changed during verification: %q", loginAgent)
	}
}

func TestChallengeFromBodySolvesDirectly(t *testing.T) {
	b := NewBackend(&testHost{}, "{}")
	solved := false
	b.client.Transport = exitRoundTripper(func(r *http.Request) (*http.Response, error) {
		response := &http.Response{StatusCode: 200, Header: make(http.Header), Request: r}
		body := "{}"
		switch {
		case r.URL.Path == "/":
			if !solved {
				t.Fatal("solver probed the host instead of solving the challenged page")
			}
			body = `<title>Firefox Accounts</title>`
		case strings.HasSuffix(r.URL.Path, "script.js"):
			body = `init ([{"ty":"clientmetrics","data":{"nested":[[1,2]]}}], "token", "/_fs-ch-test", true)`
		case strings.HasSuffix(r.URL.Path, "fst-post-back"):
			body = `{"status":"success"}`
			response.Header.Set("Set-Cookie", "clearance=accepted; Domain=firefox.com; Path=/; HttpOnly")
			solved = true
		default:
			t.Fatal("Unexpected verification request")
		}
		response.Body = io.NopCloser(strings.NewReader(body))
		return response, nil
	})
	// The account hosts travel the sign-in client now: the mock
	// answers on both, or the solver's fetches would leave the test
	// for the real network.
	b.signinClient.Transport = b.client.Transport
	page := `<title>Client Challenge</title>/_fs-ch-test`
	if err := b.challengeFromBody(context.Background(), []byte(page), "https://api.accounts.firefox.com"); err != nil {
		t.Fatal(err)
	}
	site, _ := url.Parse("https://api.accounts.firefox.com/")
	found := false
	for _, c := range b.client.Jar.Cookies(site) {
		if c.Name == "clearance" && c.Value == "accepted" {
			found = true
		}
	}
	if !found {
		t.Fatal("verification cookie not installed on API host")
	}
}

func TestIsFxAHost(t *testing.T) {
	for _, host := range []string{"api.accounts.firefox.com", "accounts.firefox.com", "oauth.accounts.firefox.com", "API.ACCOUNTS.FIREFOX.COM"} {
		if !isFxAHost(host) {
			t.Fatalf("account host not recognised: %q", host)
		}
	}
	for _, host := range []string{"vpn.mozilla.org", "firefox.settings.services.mozilla.com", "api.github.com", "accounts.firefox.com.evil.example"} {
		if isFxAHost(host) {
			t.Fatalf("non-account host recognised as account host: %q", host)
		}
	}
}

// The account service travels the sign-in client (the system's route,
// so a VPN the person already runs carries it); every other host
// keeps the protected client.
func TestFxAHostsUseSigninClient(t *testing.T) {
	b := NewBackend(&testHost{}, "{}")
	var viaSignin, viaMain int
	answer := func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	}
	b.client.Transport = exitRoundTripper(func(r *http.Request) (*http.Response, error) {
		viaMain++
		return answer(r)
	})
	b.signinClient.Transport = exitRoundTripper(func(r *http.Request) (*http.Response, error) {
		viaSignin++
		return answer(r)
	})
	ctx := context.Background()
	for _, address := range []string{
		"https://api.accounts.firefox.com/v1/recovery_email/status",
		"https://accounts.firefox.com/",
		"https://oauth.accounts.firefox.com/v1/token",
	} {
		if _, _, _, err := b.fetch(ctx, address, "GET", nil, nil); err != nil {
			t.Fatalf("account fetch %s: %v", address, err)
		}
	}
	if _, _, _, err := b.fetch(ctx, "https://vpn.mozilla.org/api/v1/fpn/test", "GET", nil, nil); err != nil {
		t.Fatalf("guardian fetch: %v", err)
	}
	if viaSignin != 3 || viaMain != 1 {
		t.Fatalf("routing wrong: viaSignin=%d viaMain=%d (want 3 and 1)", viaSignin, viaMain)
	}
}

// The sign-in client is built with the backend: a nil one would send
// the account requests nowhere.
func TestSigninClientBuilt(t *testing.T) {
	b := NewBackend(&testHost{}, "{}")
	if b.signinClient == nil || b.signinDialer == nil {
		t.Fatal("sign-in client not built")
	}
	if b.signinClient.Jar != b.client.Jar {
		t.Fatal("sign-in client does not share the cookie jar")
	}
}
