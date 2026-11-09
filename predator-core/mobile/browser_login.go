// SPDX-License-Identifier: GPL-3.0-or-later
package mobile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Browser sign-in: the app's WebView signs the person in on
// accounts.firefox.com and hands over a Firefox Accounts session token;
// the core then exchanges it for the account's OAuth tokens, the way
// FoxyVPN does (poll recovery_email/status until verified, then PKCE
// oauth/authorization -> oauth/token). Direct email/password sign-in
// (login/verify) is unchanged and stays the first attempt.

var sessionTokenPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// cleanSessionToken normalizes a handed-over session token the way
// FoxyVPN does: trim, drop whitespace/quotes, lowercase.
func cleanSessionToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	token = strings.ReplaceAll(token, " ", "")
	token = strings.ReplaceAll(token, "\"", "")
	return token
}

func validSessionToken(token string) bool {
	return sessionTokenPattern.MatchString(token)
}

// authGet is a Hawk-authenticated GET against the FxA auth server, for
// the session checks browser sign-in needs (authRequest is POST-only).
func (b *Backend) authGet(ctx context.Context, path, session string) (object, error) {
	address := "https://api.accounts.firefox.com/v1" + path
	authorization := ""
	if session != "" {
		nonce := make([]byte, 6)
		rand.Read(nonce)
		var err error
		authorization, err = hawk("GET", address, session, "", time.Now().Unix(), base64.RawURLEncoding.EncodeToString(nonce))
		if err != nil {
			return nil, err
		}
	}
	result, _, err := b.jsonRequest(ctx, address, "GET", nil, authorization)
	return result, err
}

// browserLogin exchanges a browser session token for the account's OAuth
// tokens and stores them exactly like complete() does. On any error the
// pending sign-in is cleared.
func (b *Backend) browserLogin(ctx context.Context, email, sessionToken string) (object, error) {
	sessionToken = cleanSessionToken(sessionToken)
	fail := func(err error) (object, error) {
		b.pending = nil
		return nil, err
	}
	if email == "" || !validSessionToken(sessionToken) {
		return fail(errors.New("The sign-in page did not hand over a usable session token"))
	}
	b.pending = object{"session": sessionToken, "email": email, "method": "browser"}
	// The page hands the token over right after sign-in, but the session
	// may still need its email confirmation; poll until Mozilla reports
	// it verified. A failed check (session not verified yet, or a
	// transient error) just waits for the next attempt.
	verified := false
	for attempt := 0; attempt < 100 && !verified; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fail(ctx.Err())
			case <-time.After(3 * time.Second):
			}
		}
		status, err := b.authGet(ctx, "/recovery_email/status", sessionToken)
		if err == nil && yes(status, "verified") {
			verified = true
		}
	}
	if !verified {
		return fail(errors.New("Sign-in verification timed out. Please try again."))
	}
	// PKCE, as in FoxyVPN's exchangeSessionWithOAuthCode.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fail(err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return fail(err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	auth, err := b.authRequest(ctx, "/oauth/authorization", object{
		"client_id": clientID, "response_type": "code", "scope": scope,
		"state": state, "access_type": "offline",
		"code_challenge": challenge, "code_challenge_method": "S256",
	}, sessionToken)
	if err != nil {
		return fail(err)
	}
	code := str(auth, "code")
	if code == "" || str(auth, "state") != state {
		return fail(errors.New("Invalid OAuth authorization response"))
	}
	token, err := b.jsonRequest(ctx, "https://oauth.accounts.firefox.com/v1/token", "POST", object{
		"client_id": clientID, "grant_type": "authorization_code",
		"code": code, "code_verifier": verifier,
	}, "")
	if err != nil {
		return fail(err)
	}
	if str(token, "access_token") == "" {
		return fail(errors.New("The external account service returned no access token"))
	}
	// Store exactly like complete() does.
	token["email"] = email
	expiry := num(token, "expires_in")
	if expiry == 0 {
		expiry = 86400
	}
	token["expiresAt"] = millis() + int64(expiry*1000)
	if err = b.setAuth(token); err != nil {
		return fail(err)
	}
	b.pending = nil
	// Best effort: the browser session has served its purpose.
	_, _ = b.authRequest(ctx, "/session/destroy", object{}, sessionToken)
	return object{"needsCode": false}, nil
}
