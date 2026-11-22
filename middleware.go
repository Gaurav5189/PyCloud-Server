package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const (
	ContextUserKey contextKey = "pycloud_user"
	CookieSession  string     = "pycloud_session"
	CookieCSRF     string     = "pycloud_csrf"
	HeaderCSRF     string     = "X-CSRF-Token"
)

type statusLoggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    int64
}

func (w *statusLoggingResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusLoggingResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying ResponseWriter, so
// per-request read/write deadlines still work through the logging wrapper.
func (w *statusLoggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// SecurityHeadersMiddleware adds industry-standard defense-in-depth headers
func SecurityHeadersMiddleware(tlsEnabled bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Protect against clickjacking
		h.Set("X-Frame-Options", "DENY")
		// Prevent MIME type sniffing
		h.Set("X-Content-Type-Options", "nosniff")
		// Referrer privacy
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Disable unwanted browser features
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		// Content Security Policy (strict script/style/media origins)
		csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none';"
		h.Set("Content-Security-Policy", csp)

		// HSTS if running over TLS
		if tlsEnabled {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// LoggingMiddleware logs all requests with duration and client IP
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusLoggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(sw, r)

		duration := time.Since(start)
		clientIP := ClientIP(r)

		// Do not clutter logs with high-frequency stat polls unless failed
		if strings.HasPrefix(r.URL.Path, "/api/stats") && sw.statusCode == http.StatusOK {
			return
		}

		log.Printf("[%s] %s %s %d %s %d bytes",
			clientIP, r.Method, r.URL.Path, sw.statusCode, duration.Round(time.Millisecond), sw.written)
	})
}

// bodyIdleTimeoutReader refreshes the connection read deadline before every
// read. That turns a total-request timeout into an idle timeout: a large upload
// may run for as long as it keeps making progress, but a connection that stalls
// is released instead of pinning a goroutine forever.
type bodyIdleTimeoutReader struct {
	r    io.Reader
	rc   *http.ResponseController
	idle time.Duration
}

func (b *bodyIdleTimeoutReader) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.idle))
	return b.r.Read(p)
}

func (b *bodyIdleTimeoutReader) Close() error {
	if closer, ok := b.r.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// BodyIdleTimeoutMiddleware applies a sliding read deadline to request bodies.
// http.Server.ReadTimeout cannot be used for this because it caps the entire
// request, including uploads that legitimately take minutes or hours.
func BodyIdleTimeoutMiddleware(idle time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}

		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(idle)); err != nil {
			// Not supported on this connection type (for example HTTP/2, where a
			// deadline would affect every multiplexed stream). Leave it alone.
			next.ServeHTTP(w, r)
			return
		}

		r.Body = &bodyIdleTimeoutReader{r: r.Body, rc: rc, idle: idle}
		next.ServeHTTP(w, r)
	})
}

// CSRFMiddleware generates and verifies anti-CSRF tokens
func CSRFMiddleware(tlsEnabled bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read or generate CSRF token
		var csrfToken string
		if c, err := r.Cookie(CookieCSRF); err == nil && len(c.Value) >= 32 {
			csrfToken = c.Value
		} else {
			csrfToken, _ = GenerateRandomToken(32)
			http.SetCookie(w, &http.Cookie{
				Name:     CookieCSRF,
				Value:    csrfToken,
				Path:     "/",
				HttpOnly: false, // Accessible to SPA JS to read and send back in header
				Secure:   tlsEnabled,
				SameSite: http.SameSiteStrictMode,
			})
		}

		// Enforce CSRF check on state-changing methods
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
			// Login and Setup are initial credential exchange endpoints
			if r.URL.Path == "/api/login" || r.URL.Path == "/api/setup" {
				break
			}

			clientToken := r.Header.Get(HeaderCSRF)
			if clientToken == "" {
				clientToken = r.FormValue("csrf_token")
			}

			// Validate constant-time
			if subtle.ConstantTimeCompare([]byte(clientToken), []byte(csrfToken)) != 1 {
				http.Error(w, `{"error":"invalid or missing CSRF token"}`, http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware verifies the session token and injects the user into the request context
func AuthMiddleware(cfg *Config, sm *SessionManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Public paths that do not require authentication
		if path == "/api/login" || path == "/api/setup" || path == "/api/status" ||
			path == "/login" || strings.HasPrefix(path, "/static/") || path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}

		// If initial setup is required, redirect or return setup needed
		if !cfg.IsPasswordSet() {
			if strings.HasPrefix(path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPreconditionRequired)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":        "Setup required",
					"setup_needed": true,
				})
				return
			}
			// Let the SPA handle the setup screen
			next.ServeHTTP(w, r)
			return
		}

		// Read session cookie
		cookie, err := r.Cookie(CookieSession)
		if err != nil || cookie.Value == "" {
			if strings.HasPrefix(path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "Authentication required"})
				return
			}
			// Direct browser to login in SPA
			next.ServeHTTP(w, r)
			return
		}

		username, err := sm.ValidateSessionToken(cookie.Value)
		if err != nil {
			// Clear invalid cookie
			http.SetCookie(w, &http.Cookie{
				Name:     CookieSession,
				Value:    "",
				Path:     "/",
				MaxAge:   -1,
				HttpOnly: true,
				Secure:   cfg.TLSEnabled,
				SameSite: http.SameSiteStrictMode,
			})

			if strings.HasPrefix(path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "Session expired or invalid"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Add username to context
		ctx := context.WithValue(r.Context(), ContextUserKey, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
