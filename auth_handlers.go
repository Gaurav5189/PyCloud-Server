package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type LoginRequest struct {
	Password string `json:"password"`
}

type SetupRequest struct {
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

// HandleLogin authenticates credentials with brute-force protection
func HandleLogin(cfg *Config, sm *SessionManager, rl *LoginRateLimiter, configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		ip := ClientIP(r)
		if !rl.Allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "Too many failed login attempts. Please wait 5 minutes before trying again.",
			})
			return
		}

		var req LoginRequest
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
				return
			}
		} else {
			req.Password = r.FormValue("password")
		}

		if req.Password == "" {
			http.Error(w, `{"error":"Password is required"}`, http.StatusBadRequest)
			return
		}

		// Verify constant-time
		if !VerifyPassword(req.Password, cfg.PasswordHash) {
			rl.RecordFailedAttempt(ip)
			// Small delay to impede automated timing/brute force
			time.Sleep(150 * time.Millisecond)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "Invalid password",
			})
			return
		}

		// Reset rate limiter on successful login
		rl.Reset(ip)

		// Create session token
		token, err := sm.CreateSessionToken("admin")
		if err != nil {
			http.Error(w, `{"error":"Failed to create session"}`, http.StatusInternalServerError)
			return
		}

		// Set secure session cookie
		http.SetCookie(w, &http.Cookie{
			Name:     CookieSession,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   cfg.TLSEnabled,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(SessionDuration.Seconds()),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "Authentication successful",
		})
	}
}

// HandleLogout revokes the session and clears authentication cookies
func HandleLogout(cfg *Config, sm *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(CookieSession); err == nil && cookie.Value != "" {
			sm.RevokeSession(cookie.Value)
		}

		// Expire cookie
		http.SetCookie(w, &http.Cookie{
			Name:     CookieSession,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   cfg.TLSEnabled,
			SameSite: http.SameSiteStrictMode,
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "Logged out successfully",
		})
	}
}

// HandleSetup establishes the initial admin password when first run
func HandleSetup(cfg *Config, sm *SessionManager, configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		if cfg.IsPasswordSet() {
			http.Error(w, `{"error":"Setup has already been completed"}`, http.StatusForbidden)
			return
		}

		var req SetupRequest
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"Invalid request payload"}`, http.StatusBadRequest)
				return
			}
		} else {
			req.Password = r.FormValue("password")
			req.ConfirmPassword = r.FormValue("confirm_password")
		}

		if len(req.Password) < 8 {
			http.Error(w, `{"error":"Password must be at least 8 characters"}`, http.StatusBadRequest)
			return
		}
		if req.Password != req.ConfirmPassword {
			http.Error(w, `{"error":"Passwords do not match"}`, http.StatusBadRequest)
			return
		}

		if err := cfg.SetPassword(req.Password); err != nil {
			http.Error(w, `{"error":"Failed to hash password"}`, http.StatusInternalServerError)
			return
		}

		if err := cfg.Save(configPath); err != nil {
			http.Error(w, `{"error":"Failed to save configuration"}`, http.StatusInternalServerError)
			return
		}

		// Automatically log user in upon initial setup
		token, err := sm.CreateSessionToken("admin")
		if err == nil {
			http.SetCookie(w, &http.Cookie{
				Name:     CookieSession,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				Secure:   cfg.TLSEnabled,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   int(SessionDuration.Seconds()),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "Initial setup complete",
		})
	}
}

// HandleServerStatus returns the public server operational state
func HandleServerStatus(cfg *Config, sm *SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		isLoggedIn := false
		if cookie, err := r.Cookie(CookieSession); err == nil && cookie.Value != "" {
			if _, err := sm.ValidateSessionToken(cookie.Value); err == nil {
				isLoggedIn = true
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app":            "PyCloud",
			"version":        "2.0.0",
			"setup_needed":   !cfg.IsPasswordSet(),
			"logged_in":      isLoggedIn,
			"tls_enabled":    cfg.TLSEnabled,
			"port":           cfg.Port,
			"max_upload_mb":  cfg.MaxUploadMB,
			"uptime_seconds": ServerUptimeSeconds(),
			"started_at":     serverStartTime.Unix(),
		})
	}
}
