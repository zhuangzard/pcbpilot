package console

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// TokenFile is the per-install console token, relative to the pcbpilot home.
const TokenFile = "console.token"

// Header and cookie names.
const (
	TokenHeader = "X-Pcbpilot-Token"
	CSRFHeader  = "X-Pcbpilot-Csrf"
	CookieName  = "pcbpilot_console"
)

// Home resolves the pcbpilot state dir (~/.pcbpilot; PCBPILOT_HOME overrides).
func Home() string {
	if h := os.Getenv("PCBPILOT_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".pcbpilot")
}

// EnsureToken returns the install token, creating it (0600, 32 random bytes)
// when missing.
func EnsureToken(home string) (string, error) {
	path := filepath.Join(home, TokenFile)
	if b, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(b))
		if len(tok) >= 32 {
			return tok, nil
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return tok, nil
}

// ReadToken reads the token without creating it.
func ReadToken(home string) (string, error) {
	b, err := os.ReadFile(filepath.Join(home, TokenFile))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("console token is empty")
	}
	return tok, nil
}

// loopbackHost reports whether a Host header names this machine by a loopback
// name. Rejecting everything else defeats DNS-rebinding (evil.example → 127.0.0.1).
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// originAllowed accepts no Origin (same-origin GETs, CLI) or an http origin on
// a loopback host with the same port as the request Host.
func originAllowed(origin, host string) bool {
	if origin == "" {
		return true
	}
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	if !loopbackHost(rest) {
		return false
	}
	_, op, err1 := net.SplitHostPort(rest)
	_, hp, err2 := net.SplitHostPort(host)
	if err1 != nil || err2 != nil {
		return false
	}
	return op == hp
}

// guard wraps API handlers: loopback Host, allowed Origin, and a valid token
// (header, or the session cookie plus the CSRF header on non-GET). No CORS
// headers are ever emitted, so foreign pages cannot read responses.
func (c *Console) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			jsonError(w, http.StatusForbidden, "HOST_REJECTED", "console answers loopback hosts only")
			return
		}
		if !originAllowed(r.Header.Get("Origin"), r.Host) {
			jsonError(w, http.StatusForbidden, "ORIGIN_REJECTED", "cross-origin request refused")
			return
		}
		if !c.authorized(r) {
			jsonError(w, http.StatusUnauthorized, "TOKEN_REQUIRED",
				fmt.Sprintf("missing or wrong console token — open the URL from `pcbpilot console url` (token in %s)", filepath.Join(c.home, TokenFile)))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (c *Console) authorized(r *http.Request) bool {
	tok := c.token
	if tok == "" {
		return false
	}
	if h := r.Header.Get(TokenHeader); h != "" {
		return subtle.ConstantTimeCompare([]byte(h), []byte(tok)) == 1
	}
	ck, err := r.Cookie(CookieName)
	if err != nil || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(tok)) != 1 {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(CSRFHeader) != "1" {
		return false
	}
	return true
}

// handleSession exchanges a header token for an HttpOnly SameSite=Strict
// cookie so EventSource (which cannot set headers) is authenticated.
func (c *Console) handleSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: c.token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
