// SPDX-License-Identifier: GPL-3.0-or-later
package mobile

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const prefix = "identity.mozilla.com/picl/v1/"
const clientID = "5882386c6d801776"
const scope = "profile https://identity.mozilla.com/apps/vpn"
const apiAgent = "MozillaVPN/2.35.0 (sys:android; iap:true)"

var packageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var hostName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

type serviceError struct {
	status, errno int
	message       string
}

func (e *serviceError) Error() string { return e.message }
func derive(input []byte, info string, length int) []byte {
	out := make([]byte, length)
	io.ReadFull(hkdf.New(sha256.New, input, make([]byte, 32), []byte(info)), out)
	return out
}
func passwordHash(email, password string) string {
	quick := pbkdf2.Key([]byte(password), []byte(prefix+"quickStretch:"+email), 1000, 32, sha256.New)
	return hex.EncodeToString(derive(quick, prefix+"authPW", 32))
}
func hawk(method, address, session, body string, ts int64, nonce string) (string, error) {
	raw, err := hex.DecodeString(session)
	if err != nil || len(raw) != 32 {
		return "", errors.New("Invalid sign-in session")
	}
	expanded := derive(raw, prefix+"sessionToken", 64)
	u, err := url.Parse(address)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	// A payload hash is only sent for requests with a body: FoxyVPN
	// sends none for its empty GETs, and the server rejects a hash
	// computed over a body the request does not carry.
	encoded := ""
	if body != "" {
		hash := sha256.Sum256([]byte("hawk.1.payload\napplication/json\n" + body + "\n"))
		encoded = base64.StdEncoding.EncodeToString(hash[:])
	}
	normalized := fmt.Sprintf("hawk.1.header\n%d\n%s\n%s\n%s\n%s\n%s\n%s\n\n", ts, nonce, method, u.RequestURI(), u.Hostname(), port, encoded)
	mac := hmac.New(sha256.New, expanded[32:])
	mac.Write([]byte(normalized))
	header := fmt.Sprintf(`Hawk id="%s", ts="%d", nonce="%s", mac="%s"`, hex.EncodeToString(expanded[:32]), ts, nonce, base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	if encoded != "" {
		header += fmt.Sprintf(`, hash="%s"`, encoded)
	}
	return header, nil
}
func (b *Backend) fetch(ctx context.Context, address, method string, body []byte, headers http.Header) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("User-Agent", apiAgent)
	if req.URL.Hostname() == "api.accounts.firefox.com" || req.URL.Hostname() == "accounts.firefox.com" {
		req.Header.Set("User-Agent", accountAgent)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header[k] = v
	}
	response, err := b.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if len(data) > 4*1024*1024 {
		return nil, nil, 0, errors.New("Service response is too large")
	}
	return data, response.Header, response.StatusCode, err
}
func (b *Backend) jsonRequest(ctx context.Context, address, method string, body object, authorization string) (object, http.Header, error) {
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	headers := http.Header{}
	if authorization != "" {
		headers.Set("Authorization", authorization)
	}
	for attempt := 0; attempt < 4; attempt++ {
		raw, h, status, err := b.fetch(ctx, address, method, encoded, headers)
		if err != nil {
			return nil, nil, err
		}
		if status == 406 && attempt < 3 {
			if err = b.challenge(ctx); err != nil {
				return nil, nil, err
			}
			continue
		}
		var data object
		if err = json.Unmarshal(raw, &data); err != nil {
			return nil, h, fmt.Errorf("The service returned an invalid response (HTTP %d)", status)
		}
		if status < 200 || status >= 300 {
			message := str(data, "message")
			if message == "" {
				message = fmt.Sprintf("Service request failed (HTTP %d)", status)
			}
			return nil, h, &serviceError{status, int(num(data, "errno")), message}
		}
		return data, h, nil
	}
	return nil, nil, errors.New("Service verification failed")
}
func (b *Backend) authRequest(ctx context.Context, path string, data object, session string) (object, error) {
	address := "https://api.accounts.firefox.com/v1" + path
	authorization := ""
	if session != "" {
		raw, _ := json.Marshal(data)
		nonce := make([]byte, 6)
		rand.Read(nonce)
		var err error
		authorization, err = hawk("POST", address, session, string(raw), time.Now().Unix(), base64.RawURLEncoding.EncodeToString(nonce))
		if err != nil {
			return nil, err
		}
	}
	result, _, err := b.jsonRequest(ctx, address, "POST", data, authorization)
	return result, err
}
func (b *Backend) login(ctx context.Context, email, password string) (object, error) {
	if email == "" || password == "" {
		return nil, errors.New("Enter your service account email and password")
	}
	b.pending = nil
	data := object{"email": email, "authPW": passwordHash(email, password), "verificationMethod": "email-2fa"}
	result, err := b.authRequest(ctx, "/account/login", data, "")
	var se *serviceError
	if errors.As(err, &se) && se.errno == 107 {
		delete(data, "verificationMethod")
		result, err = b.authRequest(ctx, "/account/login", data, "")
	}
	if err != nil {
		return nil, err
	}
	if str(result, "sessionToken") == "" {
		return nil, errors.New("Sign-in returned no session")
	}
	method := str(result, "verificationMethod")
	if method == "" {
		method = "email-2fa"
	}
	b.pending = object{"session": str(result, "sessionToken"), "email": email, "method": method}
	if !yes(result, "verified") || result["sessionVerified"] == false || result["emailVerified"] == false || str(result, "verificationMethod") != "" {
		return object{"needsCode": true, "verificationMethod": method}, nil
	}
	return b.complete(ctx)
}
func (b *Backend) verify(ctx context.Context, code string) (object, error) {
	if b.pending == nil {
		return nil, errors.New("Start sign-in again")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("Enter the verification code")
	}
	path := "/session/verify_code"
	if str(b.pending, "method") == "totp-2fa" {
		path = "/session/verify/totp"
	}
	_, err := b.authRequest(ctx, path, object{"code": code}, str(b.pending, "session"))
	if err != nil {
		return nil, err
	}
	return b.complete(ctx)
}
func (b *Backend) complete(ctx context.Context) (object, error) {
	token, err := b.authRequest(ctx, "/oauth/token", object{"client_id": clientID, "grant_type": "fxa-credentials", "scope": scope, "access_type": "offline"}, str(b.pending, "session"))
	if err != nil {
		return nil, err
	}
	if str(token, "access_token") == "" {
		return nil, errors.New("The external account service returned no access token")
	}
	token["email"] = str(b.pending, "email")
	expiry := num(token, "expires_in")
	if expiry == 0 {
		expiry = 86400
	}
	token["expiresAt"] = millis() + int64(expiry*1000)
	if err = b.setAuth(token); err != nil {
		return nil, err
	}
	b.pending = nil
	return object{"needsCode": false}, nil
}
func (b *Backend) token(ctx context.Context, force bool) (string, error) {
	b.authRefresh.Lock()
	defer b.authRefresh.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := b.accountID(ctx)
	b.mu.Lock()
	profile := b.profileLocked(id)
	auth := copyObject(obj(profile, "auth"))
	if id == "" {
		auth = copyObject(b.auth)
	}
	b.mu.Unlock()
	if len(auth) == 0 {
		return "", errors.New("Sign in to your service account first")
	}
	if !force && float64(millis()+60000) < num(auth, "expiresAt") {
		return str(auth, "access_token"), nil
	}
	if str(auth, "refresh_token") == "" {
		b.commitProfileAuth(id, nil)
		return "", errors.New("Your session expired. Please sign in again")
	}
	renewed, err := b.authRequest(ctx, "/oauth/token", object{"client_id": clientID, "grant_type": "refresh_token", "refresh_token": str(auth, "refresh_token"), "scope": scope}, "")
	var se *serviceError
	if err != nil {
		if errors.As(err, &se) && (se.status == 400 || se.status == 401 || se.status == 403) {
			b.commitProfileAuth(id, nil)
		}
		return "", err
	}
	if str(renewed, "access_token") == "" {
		return "", errors.New("Token refresh returned no access token")
	}
	for k, v := range renewed {
		auth[k] = v
	}
	expiry := num(renewed, "expires_in")
	if expiry == 0 {
		expiry = 86400
	}
	auth["expiresAt"] = millis() + int64(expiry*1000)
	if err = b.commitProfileAuth(id, auth); err != nil {
		return "", err
	}
	return str(auth, "access_token"), nil
}
func (b *Backend) guardian(ctx context.Context, path, method string, retry bool) (any, http.Header, error) {
	token, err := b.token(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	data, headers, err := b.jsonRequest(ctx, "https://vpn.mozilla.org/api/v1/fpn/"+path, method, nil, "Bearer "+token)
	var se *serviceError
	if errors.As(err, &se) && se.status == 401 && retry {
		if _, err = b.token(ctx, true); err != nil {
			return nil, nil, err
		}
		return b.guardian(ctx, path, method, false)
	}
	return data, headers, err
}
func (b *Backend) pass(ctx context.Context) (string, http.Header, error) {
	result, headers, err := b.guardian(ctx, "token", "GET", true)
	var se *serviceError
	if errors.As(err, &se) && se.status == 403 && !quotaEmpty(quotaHeaders(headers)) {
		if _, _, err = b.guardian(ctx, "activate", "POST", true); err != nil {
			return "", headers, err
		}
		result, headers, err = b.guardian(ctx, "token", "GET", true)
	}
	if err != nil {
		return "", headers, err
	}
	data, _ := result.(map[string]any)
	if str(data, "token") == "" {
		return "", headers, errors.New("The service returned no proxy pass")
	}
	return str(data, "token"), headers, nil
}
func (b *Backend) refreshServers(ctx context.Context) ([]object, error) {
	result, _, err := b.jsonRequest(ctx, "https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records", "GET", nil, "")
	if err != nil {
		return nil, err
	}
	servers := []object{}
	for _, value := range list(result, "data") {
		record, _ := value.(map[string]any)
		country := obj(record, "country")
		if len(country) == 0 {
			country = record
		}
		if str(country, "code") == "" || str(country, "name") == "CatchAll Anycast" {
			continue
		}
		for _, value := range list(country, "cities") {
			city, _ := value.(map[string]any)
			for _, value := range list(city, "servers") {
				server, _ := value.(map[string]any)
				if yes(server, "quarantined") {
					continue
				}
				var connect object
				protocols := list(server, "protocols")
				for _, value := range protocols {
					p, _ := value.(map[string]any)
					if str(p, "name") == "connect" {
						connect = p
						break
					}
				}
				if len(protocols) > 0 && connect == nil {
					continue
				}
				host := str(connect, "host")
				if host == "" {
					host = str(server, "hostname")
				}
				port := num(connect, "port")
				if port == 0 {
					port = num(server, "port")
				}
				if port == 0 {
					port = 443
				}
				if host == "" {
					continue
				}
				servers = append(servers, object{"id": fmt.Sprintf("%s/%s/%s:%d", str(country, "code"), str(city, "code"), host, int(port)), "country": str(country, "name"), "code": str(country, "code"), "city": displayCity(str(country, "name"), str(city, "name")), "cityCode": str(city, "code"), "host": host, "port": port})
			}
		}
	}
	if len(servers) == 0 {
		return nil, errors.New("No usable locations were received")
	}
	b.mu.Lock()
	previous := b.servers
	b.servers = servers
	b.mu.Unlock()
	if err := b.save(); err != nil {
		b.mu.Lock()
		b.servers = previous
		b.mu.Unlock()
		return nil, err
	}
	b.emit()
	return servers, nil
}

var challengePath = regexp.MustCompile(`/_fs-ch-[A-Za-z0-9]+`)

func solvePow(base, hash string) (string, error) {
	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for _, a := range alphabet {
		for _, z := range alphabet {
			answer := string(a) + string(z)
			h := sha256.Sum256([]byte(base + answer))
			if hex.EncodeToString(h[:]) == hash {
				return answer, nil
			}
		}
	}
	return "", errors.New("Unable to complete service verification")
}

// The Firefox Accounts hosts the challenge solver tries, and the user
// agents it tries on each: the bot protection judges a request by both,
// so a challenge cleared under one agent may not clear under another
// (FoxyVPN's FastlyChallengeSolver tries the same combinations).
var challengeHosts = []string{"https://api.accounts.firefox.com", "https://accounts.firefox.com", "https://oauth.accounts.firefox.com"}

const firefoxAndroidAgent = "Mozilla/5.0 (Android 14; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0"

func (b *Backend) challenge(ctx context.Context) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	// Reuse the physical-network transport but start with a fresh challenge jar.
	solver := &Backend{client: &http.Client{Transport: b.client.Transport, Jar: jar, Timeout: b.client.Timeout, CheckRedirect: b.client.CheckRedirect}}
	if err := solver.solveChallenge(ctx); err != nil {
		return err
	}
	// Every cookie the solve issued, collected once: a challenge solved
	// on one host must clear the others too, so a copy is installed for
	// each FxA host (and a firefox.com domain copy), the way FoxyVPN
	// does. A plain per-host copy would leave oauth.accounts.firefox.com
	// challenging when the solve happened on api.accounts.firefox.com.
	hosts := []string{"https://api.accounts.firefox.com/", "https://accounts.firefox.com/", "https://oauth.accounts.firefox.com/"}
	var issued []*http.Cookie
	seen := map[string]bool{}
	for _, address := range hosts {
		u, _ := url.Parse(address)
		for _, c := range jar.Cookies(u) {
			key := c.Name + "\x00" + c.Value
			if seen[key] {
				continue
			}
			seen[key] = true
			issued = append(issued, c)
		}
	}
	if len(issued) == 0 {
		return errors.New("Service verification completed but issued no cookies. Try again later")
	}
	for _, address := range hosts {
		u, _ := url.Parse(address)
		for _, c := range issued {
			clone := *c
			clone.Domain = u.Hostname()
			b.client.Jar.SetCookies(u, []*http.Cookie{&clone})
		}
	}
	u, _ := url.Parse("https://firefox.com/")
	for _, c := range issued {
		clone := *c
		clone.Domain = "firefox.com"
		b.client.Jar.SetCookies(u, []*http.Cookie{&clone})
	}
	return nil
}

// isChallengePage reports whether a fetched page is a Fastly client
// challenge page: it carries a challenge asset prefix and one of the
// challenge markers.
func isChallengePage(page []byte) bool {
	if len(page) == 0 || len(page) > 2*1024*1024 {
		return false
	}
	if !challengePath.Match(page) {
		return false
	}
	lower := bytes.ToLower(page)
	return bytes.Contains(lower, []byte("client challenge")) || bytes.Contains(page, []byte("script.js")) || bytes.Contains(page, []byte("fst-post-back"))
}

func (b *Backend) solveChallenge(ctx context.Context) error {
	agents := []string{accountAgent, apiAgent, firefoxAndroidAgent}
	var lastErr error
	for _, base := range challengeHosts {
		// Probe with the account agent only: a host that is not
		// challenging must never see the other agents, so the
		// account's own agent stays the one used for the account.
		served, err := b.solveChallengeOnHost(ctx, base, accountAgent)
		if err == nil {
			if served {
				return nil
			}
			continue
		}
		// A challenge was served but the account agent could not
		// solve it: try the other agents on this host.
		lastErr = err
		for _, agent := range agents[1:] {
			served, err := b.solveChallengeOnHost(ctx, base, agent)
			if err != nil {
				lastErr = err
				continue
			}
			if served {
				return nil
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("Could not complete service verification. Try again later")
}

// solveChallengeOnHost solves the Fastly challenge on one host with one
// user agent. It reports whether the host served a challenge page at
// all: false means there was nothing to solve here, and the caller tries
// the next host. A real failure while solving is an error.
func (b *Backend) solveChallengeOnHost(ctx context.Context, base, agent string) (bool, error) {
	headers := http.Header{"User-Agent": []string{agent}, "Accept": []string{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}}
	page, _, _, err := b.fetch(ctx, base+"/", "GET", nil, headers)
	if err != nil {
		return false, nil
	}
	if !isChallengePage(page) {
		return false, nil
	}
	if err := b.solveChallengePage(ctx, base, agent, string(page)); err != nil {
		return true, err
	}
	return true, nil
}

func (b *Backend) solveChallengePage(ctx context.Context, base, agent, page string) error {
	headers := http.Header{"User-Agent": []string{agent}, "Accept": []string{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}}
	path := challengePath.FindString(page)
	if path == "" {
		return errors.New("Service verification could not load. Try again")
	}
	root := base + path
	script, _, status, err := b.fetch(ctx, root+"/script.js?reload=true", "GET", nil, headers)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("Service verification could not load (HTTP %d). Try again", status)
	}
	challenges, token, err := parseChallengeInit(string(script), path)
	if err != nil {
		return err
	}
	for round := 0; round < 8; round++ {
		answers := []object{}
		for _, c := range challenges {
			switch str(c, "ty") {
			case "pow":
				d := obj(c, "data")
				answer, e := solvePow(str(d, "base"), str(d, "hash"))
				if e != nil {
					return e
				}
				answers = append(answers, object{"ty": "pow", "base": d["base"], "answer": answer, "hmac": d["hmac"], "expires": d["expires"]})
			case "pat":
				raw, _, status, e := b.fetch(ctx, root+"/pat?token="+url.QueryEscape(token), "POST", nil, headers)
				if e != nil {
					return e
				}
				auth := ""
				if status != 400 && status != 401 {
					var d object
					json.Unmarshal(raw, &d)
					auth = str(d, "auth")
				}
				answers = append(answers, object{"ty": "pat", "auth": auth})
			case "clientmetrics":
				answers = append(answers, object{"ty": "clientmetrics", "webdriver": false, "bot_detection_result": object{"bot_detected": false, "bot_kind": nil}, "browser_metrics": object{"client_data": "{}", "error_trace": nil}, "detector_results": object{}, "v": 2})
			case "captcha":
				return errors.New("Mozilla requires an interactive challenge (CAPTCHA). Please sign in again in the browser page.")
			default:
				return errors.New("The service requires an interactive challenge. Try again later")
			}
		}
		encoded, _ := json.Marshal(object{"token": token, "data": answers})
		postHeaders := headers.Clone()
		postHeaders.Set("Origin", base)
		postHeaders.Set("Accept", "application/json")
		postHeaders.Set("Content-Type", "application/json")
		raw, _, status, e := b.fetch(ctx, root+"/fst-post-back", "POST", encoded, postHeaders)
		if e != nil {
			return e
		}
		if status != 200 {
			return errors.New("Service verification failed")
		}
		var d object
		if json.Unmarshal(raw, &d) != nil {
			return errors.New("Invalid service verification response")
		}
		if str(d, "status") == "success" {
			// The cookies only count once the host stops challenging:
			// re-fetch the root and confirm the challenge page is gone.
			again, _, _, err := b.fetch(ctx, base+"/", "GET", nil, headers)
			if err != nil {
				return err
			}
			if isChallengePage(again) {
				return errors.New("Service verification was not accepted. Try again later")
			}
			return nil
		}
		challenges = nil
		for _, v := range list(d, "ch") {
			c, _ := v.(map[string]any)
			challenges = append(challenges, c)
		}
		token = str(d, "tok")
		if token == "" || len(challenges) == 0 {
			return errors.New("Service verification did not complete")
		}
	}
	return errors.New("Service verification did not complete")
}

func displayCity(country, city string) string {
	city = strings.TrimSpace(city)
	if strings.EqualFold(city, strings.TrimSpace(country)) {
		return ""
	}
	return city
}
func locationLabel(server object) string {
	country := strings.TrimSpace(str(server, "country"))
	city := displayCity(country, str(server, "city"))
	if city == "" {
		return country
	}
	if country == "" {
		return city
	}
	return country + ", " + city
}

var cachedHost = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)
