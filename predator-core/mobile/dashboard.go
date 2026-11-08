// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 — merged project: PredatorVPN core + Arman VPN UI.
//
// dashboard.go — Arman-compatible HTTP dashboard on top of the Predator
// Backend.
//
// INSTALL: copy this file into predator/android/core/mobile/dashboard.go
// (same package `mobile`). It reuses the helpers already defined in that
// package: object, str(), num(), yes(), obj(), list(), copyObject() and
// envelope(). Do NOT copy those helpers here.
//
// WHY THIS FILE EXISTS
// Arman Flutter UI (app/lib/core/core_api.dart) talks to its Go core over
// HTTP on 127.0.0.1:10820: GET /api/status, POST /api/signin, ...
// Predator's Android core has no HTTP API at all: its UI calls
// Backend.Call(channel, argsJSON) and reads Backend.Snapshot().
// This file is the translation layer between the two contracts, so the
// Flutter UI can stay (almost) unchanged while the engine underneath is
// Predator's.
//
// WHAT IT DOES / DOES NOT DO
//   - Translates Snapshot() into Arman's CoreStatus JSON (see MERGE-ANALYSIS.md §3).
//   - Maps sign-in / accounts / locations / settings / power onto Backend.Call.
//   - Returns 501 with a clear message for things Predator has no
//     equivalent for (browser sign-in — removed in Predator v2.0.0,
//     backup export, live connection list, update download).
//   - Does NOT replace FoxVpnService / the sing-box TUN stack. The TUN
//     state in /api/status is reported from the Backend snapshot only;
//     wiring the real VpnService state in is a TODO marked below.
//   - Binds loopback only. Never bind 0.0.0.0.
//   - Requires the X-Local-Token header on every request (see
//     StartDashboard): Arman's Flutter UI reads the token from
//     <dataDir>/local-api.token and sends it on every call
//     (app/lib/core/core_api.dart). The token is generated fresh on each
//     app start by PredatorBridge and written to that file BEFORE the
//     dashboard starts listening, so a stale listener can never be
//     trusted with an old token — the same rule Arman's own core uses.
//
// STATUS: written from the contracts in the two repos, NOT compiled here —
// this machine has no Go toolchain. Run `go vet ./mobile` in
// predator-core before trusting it.
package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dashboard is the HTTP adapter. One Backend, one dashboard.
type dashboard struct {
	b       *Backend
	mu      sync.Mutex
	signins map[string]object // id -> {"email":..., "method":...}
	next    int
	// token is the X-Local-Token value the Flutter UI must send. It is
	// generated per app start by the Android host and written to
	// <dataDir>/local-api.token before StartDashboard is called.
	token string
}

// requireToken rejects requests without the current local token. Every
// app on a phone can reach loopback, so the token — not the loopback
// address — is what keeps other apps out of this API.
func (d *dashboard) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.token == "" || r.Header.Get("X-Local-Token") != d.token {
			writeErr(w, http.StatusUnauthorized, "A local token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *dashboard) snapshot() object {
	var snap object
	_ = json.Unmarshal([]byte(d.b.Snapshot()), &snap)
	if snap == nil {
		snap = object{}
	}
	return snap
}

// call runs Backend.Call and decodes its {"ok":bool,"data":...,"error":...} envelope.
func (d *dashboard) call(channel string, args any) (any, error) {
	rawArgs, err := json.Marshal([]any{args})
	if err != nil {
		return nil, err
	}
	// Callers that need "no args" pass nil and get "[null]"; Backend.Call
	// ignores extra args for those channels, except where noted.
	if args == nil {
		rawArgs = []byte("[]")
	}
	var env object
	if err := json.Unmarshal([]byte(d.b.Call(channel, string(rawArgs))), &env); err != nil {
		return nil, err
	}
	if !yes(env, "ok") {
		msg := str(env, "error")
		if msg == "" {
			msg = "Backend call failed: " + channel
		}
		return nil, errors.New(msg)
	}
	return env["data"], nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, object{"error": msg})
}

func readBody(r *http.Request) object {
	var body object
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = object{}
	}
	return body
}

// --- Snapshot -> Arman CoreStatus translation ---

func predatorPhase(connection string) string {
	switch connection {
	case "connected":
		return "running"
	case "connecting":
		return "starting"
	case "disconnected":
		return "off"
	default:
		return "error"
	}
}

// countryFromServerID turns Predator server ids ("DE/BER/host:443") into a
// country code ("DE"). "REC" (recommended) maps to "" in Arman's selection.
func countryFromServerID(id string) string {
	if id == "" || id == "REC" {
		return ""
	}
	if i := strings.Index(id, "/"); i > 0 {
		return id[:i]
	}
	return id
}

func (d *dashboard) statusJSON() object {
	snap := d.snapshot()
	settings := obj(snap, "settings")
	connection := str(snap, "connection")
	servers := list(snap, "servers")

	// Active account: match activeAccountId. NOTE — Predator's
	// publicAccountsLocked() returns only id/email/active/status/quota/
	// checkedAt: there is no "name" or "position", so name falls back to
	// the email and position is the index in the list.
	var active any
	activeID := str(snap, "activeAccountId")
	for i, v := range list(snap, "accounts") {
		acc, _ := v.(map[string]any)
		if acc == nil {
			continue
		}
		if str(acc, "id") == activeID {
			accQuota := obj(acc, "quota")
			active = object{
				"id":           str(acc, "id"),
				"name":         str(acc, "email"),
				"email":        str(acc, "email"),
				"position":     i,
				"enabled":      true,
				"active":       true,
				"status":       str(acc, "status"),
				"usage":        accQuota,
				"used_percent": quotaUsedPercent(accQuota),
			}
			break
		}
	}

	// Proxy info — Arman's ProxyInfo shape (models.dart), NOT Predator's
	// fields: {listen, upstream, country_code, country_name, city_name,
	// selection_source, transport, started_at, login_required}. The
	// listen address is Predator's SOCKS proxy (default 127.0.0.1:1080).
	bind := str(settings, "bind")
	if bind == "" {
		bind = "127.0.0.1"
	}
	port := int(num(settings, "socksPort"))
	if port == 0 {
		port = 1080
	}
	activeServer := obj(snap, "activeServer")
	upstream := ""
	if u := obj(settings, "upstream"); yes(u, "enabled") {
		upstream = str(u, "host")
	}
	startedAt := ""
	if ms := int64(num(snap, "startedAt")); ms > 0 {
		startedAt = time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	proxy := object{
		"listen":           fmt.Sprintf("%s:%d", bind, port),
		"upstream":         upstream,
		"country_code":     str(activeServer, "code"),
		"country_name":     str(activeServer, "country"),
		"city_name":        str(activeServer, "city"),
		"selection_source": func() string {
			if str(settings, "serverId") == "REC" {
				return "recommended"
			}
			return "manual"
		}(),
		"transport":      str(settings, "transport"),
		"started_at":     startedAt,
		"login_required": !yes(snap, "signedIn"),
	}

	// Selection: REC -> "" (recommended), otherwise country code.
	serverID := str(settings, "serverId")
	selection := countryFromServerID(serverID)
	selectionName := ""
	for _, v := range servers {
		srv, _ := v.(map[string]any)
		if srv != nil && str(srv, "id") == serverID {
			selectionName = str(srv, "country")
			break
		}
	}

	// Traffic: session up/down are in the snapshot; rates are not
	// reported by Predator, so leave them out rather than inventing them.
	traffic := object{
		"up":   int64(num(snap, "up")),
		"down": int64(num(snap, "down")),
	}

	// Exit info, when the tunnel has reported one.
	var exitInfo any
	if e := obj(snap, "exit"); len(e) > 0 {
		exitInfo = object{
			"ipv4":         str(e, "ipv4"),
			"ipv6":         str(e, "ipv6"),
			"country_code": str(e, "country_code"),
			"country_name": str(e, "country_name"),
			"city":         str(e, "city"),
			"network":      str(e, "network"),
		}
	}

	// Account counts: Arman reads accountCounts['total'] and
	// accountCounts['ready'] (CoreStatus.totalAccounts/readyAccounts).
	readyCount := 0
	for _, v := range list(snap, "accounts") {
		if acc, ok := v.(map[string]any); ok && str(acc, "status") == "ready" {
			readyCount++
		}
	}

	return object{
		"state": object{
			"phase":   predatorPhase(connection),
			"message": str(snap, "lastError"),
			"code":    "",
			"address": "",
		},
		"proxy":                  proxy,
		"active_account":         active,
		"exit":                   exitInfo,
		"traffic":                traffic,
		"notices":                []any{},
		"accounts":               object{"total": len(list(snap, "accounts")), "ready": readyCount},
		"switch_threshold_percent": 0,
		"version":                str(snap, "version"),
		"can_exit":               true,
		"system_proxy":           "",
		"system_proxy_supported": false,
		// TODO: report the real FoxVpnService TUN state here. The snapshot
		// has no TUN field; "off"/"not_running" below is only correct
		// while disconnected.
		"tun_supported": true,
		"tun": object{
			"state": tunState(connection), "code": tunCode(connection),
			"message": "", "service_version": "", "installed": true,
			"programs_supported": false, "refused_udp": "",
		},
		"update":         object{"state": "idle", "version": "", "done": 0, "total": 0, "error": ""},
		"auto_connecting": false,
		"connect_pending": yes(snap, "busy"),
		"connect_failure": func() string {
			if connection == "disconnected" {
				return str(snap, "lastError")
			}
			return ""
		}(),
		"selection":      selection,
		"selection_name": selectionName,
		"data_dir":       "",
	}
}

func tunState(connection string) string {
	if connection == "connected" {
		return "on"
	}
	return "off"
}

func tunCode(connection string) string {
	if connection == "connected" {
		return ""
	}
	return "not_running"
}

// quotaUsedPercent derives a percentage from Predator's quota object.
// Predator's quota is {limit, remaining, reset, unlimited} where limit /
// remaining are the x-quota-limit / x-quota-remaining header strings
// (see backend.go:552 and refreshProfile), NOT numbers. Parse them as
// floats; used = limit - remaining. Returns 0 for unlimited quotas or
// when the numbers are absent, so the UI shows "unknown" instead of a
// fabricated number.
func quotaUsedPercent(q object) float64 {
	if yes(q, "unlimited") {
		return 0
	}
	limit := quotaNumber(q, "limit")
	remaining := quotaNumber(q, "remaining")
	if limit <= 0 {
		return 0
	}
	used := limit - remaining
	if used < 0 {
		used = 0
	}
	return used / limit * 100
}

func quotaNumber(q object, key string) float64 {
	if v, ok := q[key].(string); ok {
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	}
	return num(q, key)
}

// --- Handlers ---

func (d *dashboard) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	writeJSON(w, http.StatusOK, d.statusJSON())
}

func (d *dashboard) handleAccounts(w http.ResponseWriter, r *http.Request) {
	snap := d.snapshot()
	accounts := []any{}
	for i, v := range list(snap, "accounts") {
		acc, _ := v.(map[string]any)
		if acc == nil {
			continue
		}
		accQuota := obj(acc, "quota")
		accounts = append(accounts, object{
			"id":           str(acc, "id"),
			"name":         str(acc, "email"),
			"email":        str(acc, "email"),
			"position":     i,
			"enabled":      true,
			"active":       str(acc, "id") == str(snap, "activeAccountId"),
			"status":       str(acc, "status"),
			"usage":        accQuota,
			"used_percent": quotaUsedPercent(accQuota),
			"has_password": false,
		})
	}
	writeJSON(w, http.StatusOK, object{
		"accounts":                 accounts,
		"switch_threshold_percent": 0,
	})
}

// locationsJSON builds Arman's flat Location list (models_more.dart:
// Location{code, name, city, server, recommended, ping_ms, ping_error}).
// Arman's servers page expects ONE entry per country plus a special
// "REC" entry for the recommended location — NOT a grouped
// country->cities object. Predator's servers are per-host
// ({id, country, code, city, cityCode, host, port}), so collapse them
// to one entry per country code, keeping the first city/host and the
// best (lowest) measured latency for that country.
func (d *dashboard) locationsJSON() object {
	snap := d.snapshot()
	lat := obj(snap, "latencies")
	byCountry := map[string]object{}
	order := []string{}
	for _, v := range list(snap, "servers") {
		srv, _ := v.(map[string]any)
		if srv == nil {
			continue
		}
		code := str(srv, "code")
		if code == "" {
			continue
		}
		ping := int64(num(lat, str(srv, "id")))
		if cur, ok := byCountry[code]; ok {
			if ping > 0 && (int64(num(cur, "ping_ms")) == 0 || ping < int64(num(cur, "ping_ms"))) {
				cur["ping_ms"] = ping
			}
			continue
		}
		byCountry[code] = object{
			"code": code, "name": str(srv, "country"),
			"city": str(srv, "city"), "server": str(srv, "host"),
			"recommended": false, "ping_ms": ping, "ping_error": "",
		}
		order = append(order, code)
	}
	sort.Strings(order)
	locations := []any{object{
		"code": "REC", "name": "", "city": "", "server": "",
		"recommended": true, "ping_ms": 0, "ping_error": "",
	}}
	for _, code := range order {
		locations = append(locations, byCountry[code])
	}
	return object{"locations": locations, "pinged_at": ""}
}

func (d *dashboard) handleLocations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.locationsJSON())
}

func (d *dashboard) handlePingLocations(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	codes := []string{}
	for _, v := range list(body, "codes") {
		if s, ok := v.(string); ok {
			codes = append(codes, s)
		}
	}
	snap := d.snapshot()
	// Predator pings one server id at a time; ping the first server of
	// each requested country. "REC" is skipped: it is not a real server.
	for _, code := range codes {
		if code == "REC" {
			continue
		}
		for _, v := range list(snap, "servers") {
			srv, _ := v.(map[string]any)
			if srv != nil && str(srv, "code") == code {
				_, _ = d.call("ping", str(srv, "id"))
				break
			}
		}
	}
	// Re-read latencies through locationsJSON() after the pings.
	writeJSON(w, http.StatusOK, d.locationsJSON())
}

func (d *dashboard) handleSetLocation(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	country := str(body, "country")
	if country == "" || country == "REC" {
		// Recommended location.
		if _, err := d.call("select", "REC"); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, object{})
		return
	}
	if country == "fastest" {
		// Predator has no "fastest" selection; use the lowest measured
		// latency, falling back to REC when nothing was measured yet.
		snap := d.snapshot()
		lat := obj(snap, "latencies")
		bestID, bestMs := "", int64(0)
		for _, v := range list(snap, "servers") {
			srv, _ := v.(map[string]any)
			if srv == nil {
				continue
			}
			ms := int64(num(lat, str(srv, "id")))
			if ms > 0 && (bestMs == 0 || ms < bestMs) {
				bestID, bestMs = str(srv, "id"), ms
			}
		}
		target := "REC"
		if bestID != "" {
			target = bestID
		}
		if _, err := d.call("select", target); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, object{})
		return
	}
	snap := d.snapshot()
	for _, v := range list(snap, "servers") {
		srv, _ := v.(map[string]any)
		if srv != nil && str(srv, "code") == country {
			if _, err := d.call("select", str(srv, "id")); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, object{})
			return
		}
	}
	writeErr(w, http.StatusBadRequest, "Unknown country: "+country)
}

func (d *dashboard) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Arman's CoreApi.settings() requires view['config'] to be a map
		// (core_api.dart) — a bare settings object throws
		// 'settings without config'. Wrap it.
		snap := d.snapshot()
		writeJSON(w, http.StatusOK, object{"config": obj(snap, "settings")})
		return
	}
	if r.Method == http.MethodPut {
		body := readBody(r)
		// Arman sends {"config": {...}}; accept a bare settings object too.
		cfg := obj(body, "config")
		if len(cfg) == 0 {
			cfg = body
		}
		if _, err := d.call("settings", cfg); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, object{})
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "GET or PUT only")
}

func (d *dashboard) handlePower(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	if yes(body, "on") {
		if _, err := d.call("connect", nil); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if _, err := d.call("disconnect", nil); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, object{})
}

func (d *dashboard) handleLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	snap := d.snapshot()
	// Arman expects {"entries": [...]} (core_api.dart logs()), each with
	// an integer id used for pagination (logs_page.dart: _after =
	// more.last.id). Predator's logs have no id, so use the index.
	// "time" is already RFC 3339 in the snapshot.
	entries := []any{}
	for i, v := range list(snap, "logs") {
		if i <= after {
			continue
		}
		m, _ := v.(map[string]any)
		if m == nil {
			continue
		}
		entries = append(entries, object{
			"id":      i,
			"time":    str(m, "time"),
			"level":   str(m, "level"),
			"message": str(m, "message"),
		})
	}
	writeJSON(w, http.StatusOK, object{"entries": entries})
}

func (d *dashboard) handleTest(w http.ResponseWriter, r *http.Request) {
	// Predator has no single "connection test" endpoint; the closest
	// observable is the exit check after connecting.
	data, err := d.call("exit", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, object{
			"at": time.Now().UTC().Format(time.RFC3339), "ok": false,
			"error": err.Error(), "error_code": "unreachable",
		})
		return
	}
	exit, _ := data.(map[string]any)
	writeJSON(w, http.StatusOK, object{
		"at": time.Now().UTC().Format(time.RFC3339), "ok": true,
		"exit_ipv4":    str(exit, "ipv4"),
		"exit_ipv6":    str(exit, "ipv6"),
		"exit_country": str(exit, "country_name"),
		"exit_city":    str(exit, "city"),
	})
}

// --- Sign-in: Predator login/verify -> Arman SignInState ---

func (d *dashboard) handleStartSignIn(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)
	email := str(body, "email")
	password := str(body, "password")
	data, err := d.call("login", object{"email": email, "password": password})
	if err != nil {
		writeJSON(w, http.StatusOK, object{
			"id": "", "email": email, "step": "failed",
			"error": err.Error(), "error_code": signInErrorCode(err),
		})
		return
	}
	res, _ := data.(map[string]any)
	d.mu.Lock()
	d.next++
	id := fmt.Sprintf("predator-%d", d.next)
	if d.signins == nil {
		d.signins = map[string]object{}
	}
	method := "email-2fa"
	if res != nil {
		method = str(res, "verificationMethod")
	}
	d.signins[id] = object{"email": email, "method": method}
	d.mu.Unlock()

	if res != nil && yes(res, "needsCode") {
		step := "need_email_code"
		if method == "totp-2fa" {
			step = "need_totp"
		}
		writeJSON(w, http.StatusOK, object{
			"id": id, "email": email, "step": step,
			"password_accepted": true,
		})
		return
	}
	// No code needed: login completed.
	writeJSON(w, http.StatusOK, object{
		"id": id, "email": email, "step": "done",
		"password_accepted": true,
	})
}

func signInErrorCode(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "password"):
		return "wrong_password"
	case strings.Contains(msg, "too many"):
		return "too_many_attempts"
	case strings.Contains(msg, "network"), strings.Contains(msg, "timeout"):
		return "network"
	default:
		return ""
	}
}

func (d *dashboard) handleSignInState(w http.ResponseWriter, r *http.Request, id string) {
	d.mu.Lock()
	s, ok := d.signins[id]
	d.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "Unknown sign-in")
		return
	}
	// Predator keeps no pollable sign-in state; the state is whatever
	// step we last returned. Re-report need-code until answered.
	method := str(s, "method")
	step := "need_email_code"
	if method == "totp-2fa" {
		step = "need_totp"
	}
	writeJSON(w, http.StatusOK, object{
		"id": id, "email": str(s, "email"), "step": step,
		"password_accepted": true,
	})
}

func (d *dashboard) handleAnswerSignIn(w http.ResponseWriter, r *http.Request, id string) {
	body := readBody(r)
	code := str(body, "value")
	if _, err := d.call("verify", code); err != nil {
		writeJSON(w, http.StatusOK, object{
			"id": id, "step": "failed",
			"error": err.Error(), "error_code": "wrong_code",
		})
		return
	}
	d.mu.Lock()
	delete(d.signins, id)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, object{"id": id, "step": "done"})
}

// StartDashboard serves the Arman-compatible API on listen (use
// "127.0.0.1:10820" — the address Arman's CoreLocator expects by default).
// token is the X-Local-Token the Flutter UI must send; the Android host
// generates it fresh on each start and writes it to
// <dataDir>/local-api.token BEFORE calling this. An empty token is
// refused: an unauthenticated dashboard on loopback would let any app
// on the phone drive the VPN.
// It runs in the background; the returned error is only for listen/setup
// failures. Call it once, after NewBackend, from the Android host
// (replacing mobile.Mobile.Start in Arman's MainActivity/CoreService flow).
func (b *Backend) StartDashboard(listen string, token string) error {
	if token == "" {
		return errors.New("StartDashboard needs a non-empty local token")
	}
	d := &dashboard{b: b, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", d.handleStatus)
	mux.HandleFunc("/api/accounts", d.handleAccounts)
	mux.HandleFunc("/api/locations", d.handleLocations)
	mux.HandleFunc("/api/locations/ping", d.handlePingLocations)
	mux.HandleFunc("/api/location", d.handleSetLocation)
	mux.HandleFunc("/api/settings", d.handleSettings)
	mux.HandleFunc("/api/power", d.handlePower)
	// System proxy: Predator has no system-proxy mode (its proxy-only
	// mode is the equivalent of "clear"). The Flutter UI hides this
	// toggle when system_proxy_supported is false, so this should
	// rarely be called; answer explicitly instead of 404ing.
	mux.HandleFunc("/api/system-proxy", func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if str(body, "mode") == "clear" {
			writeJSON(w, http.StatusOK, object{})
			return
		}
		writeErr(w, http.StatusBadRequest, "System proxy mode is not supported by the Predator core; use VPN mode.")
	})
	mux.HandleFunc("/api/app/exit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, object{})
	})
	mux.HandleFunc("/api/logs", d.handleLogs)
	mux.HandleFunc("/api/test", d.handleTest)
	mux.HandleFunc("/api/signin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			d.handleStartSignIn(w, r)
			return
		}
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
	})
	// /api/signin/{id}: GET state, POST answer, DELETE cancel.
	mux.HandleFunc("/api/signin/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/signin/")
		if id == "" || strings.Contains(id, "/") {
			writeErr(w, http.StatusNotFound, "Unknown sign-in")
			return
		}
		switch r.Method {
		case http.MethodGet:
			d.handleSignInState(w, r, id)
		case http.MethodPost:
			d.handleAnswerSignIn(w, r, id)
		case http.MethodDelete:
			_, _ = d.call("cancelAccountLogin", nil)
			d.mu.Lock()
			delete(d.signins, id)
			d.mu.Unlock()
			writeJSON(w, http.StatusOK, object{})
		default:
			writeErr(w, http.StatusMethodNotAllowed, "GET, POST or DELETE only")
		}
	})
	// Explicitly unsupported, with honest errors instead of silent stubs.
	unsupported := func(msg string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotImplemented, msg)
		}
	}
	mux.HandleFunc("/api/signin/browser", unsupported("Browser sign-in was removed in Predator v2.0.0; use email/password sign-in."))
	mux.HandleFunc("/api/signin/browser/proxy", unsupported("Browser sign-in was removed in Predator v2.0.0."))
	mux.HandleFunc("/api/connections", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []any{})
	})
	mux.HandleFunc("/api/backup/export", unsupported("Backup export is not implemented for Predator's encrypted vault yet."))
	mux.HandleFunc("/api/update/download", unsupported("Updates for the merged app are not wired to Predator's release feed; wire your own feed first."))
	mux.HandleFunc("/api/routing", func(w http.ResponseWriter, r *http.Request) {
		// Split tunneling lives in Predator's settings, not a routing API.
		// Return Arman's RoutingInfo shape (models_more.dart), not
		// Arman's AppRouting shape: enabled/active/summary/files/etc.
		// The app list itself is edited through /api/settings
		// (settings.splitMode / excludedApps), not here.
		snap := d.snapshot()
		settings := obj(snap, "settings")
		apps := list(settings, "excludedApps")
		mode := str(settings, "splitMode")
		summary := "All apps use the VPN except the selected apps."
		if mode == "include" {
			summary = "Only the selected apps use the VPN."
		}
		writeJSON(w, http.StatusOK, object{
			"enabled":      len(apps) > 0,
			"region_rules": []any{},
			"active":       str(snap, "connection") == "connected",
			"summary":      summary,
			"error":        "",
			"updating":     false,
			"last_error":   "",
			"files":        []any{},
			"source":       "",
			"sources":      []any{},
		})
	})

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	go func() {
		_ = http.Serve(ln, d.requireToken(mux))
	}()
	return nil
}
