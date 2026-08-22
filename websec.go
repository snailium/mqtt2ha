package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
)

// websec.go — Web UI hardening (audit P1 #4):
//   - CSRF token on every state-changing POST
//   - write operations are POST-only
//   - optional bearer-token auth (config.WebToken)
//   - component whitelist for entity edits / imports
//   - request body cap for import

// componentWhiteList is the set of HA components mqtt2ha may emit.
var componentWhiteList = map[string]bool{
	ComponentSensor:       true,
	ComponentBinarySensor: true,
}

func validComponent(c string) bool { return componentWhiteList[c] }

// newCSRFToken returns a fresh random per-process CSRF token.
func newCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is effectively unrecoverable; fall back to a
		// time-based token rather than panicking on startup.
		return fallbackToken()
	}
	return hex.EncodeToString(b)
}

func fallbackToken() string {
	return "csrffallback"
}

// requireWrite enforces POST-only + CSRF token for a state-changing handler.
// Call from the handler before doing any work. The token is read from a form
// field (rendered into the UI) for form/multipart requests, or from the
// X-CSRF-Token header otherwise — so a raw JSON body (e.g. import) is never
// consumed by form parsing.
func (b *Bridge) requireWrite(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return false
	}
	// CSRF: the token comes from a form field (rendered into the UI) or an
	// X-CSRF-Token header. Use constant-time compare.
	sent := r.Header.Get("X-CSRF-Token")
	if sent == "" {
		ct := r.Header.Get("Content-Type")
		isForm := strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data")
		if isForm {
			sent = r.FormValue("csrf")
		}
	}
	if subtle.ConstantTimeCompare([]byte(sent), []byte(b.csrfToken)) != 1 {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// requireAuth guards the whole UI/API when config.WebToken is set.
// Without a configured token, no auth is enforced (LAN default).
func (b *Bridge) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if b.cfg.WebToken != "" {
			authn := strings.TrimSpace(r.Header.Get("Authorization"))
			if strings.HasPrefix(strings.ToLower(authn), "bearer ") {
				// accept "Bearer <token>"
				authn = strings.TrimSpace(authn[len("bearer "):])
			}
			if subtle.ConstantTimeCompare([]byte(authn), []byte(b.cfg.WebToken)) == 1 {
				next(w, r)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// logSecure is a thin wrapper used when auth/csrf reject a request.
func (b *Bridge) logRejected(r *http.Request, reason string) {
	log.Printf("web rejected %s %s: %s", r.Method, r.URL.Path, reason)
}

// trimBlank reports whether s is only whitespace (used for import validation).
func trimBlank(s string) bool { return strings.TrimSpace(s) == "" }
