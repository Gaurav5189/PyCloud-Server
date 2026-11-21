package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestServer(t *testing.T) (*Config, *SessionManager, *LoginRateLimiter, *TaskManager, *StatsCollector, http.Handler, string) {
	tempDir, err := os.MkdirTemp("", "pycloud_srv_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	configPath := filepath.Join(tempDir, "config.json")
	cfg := DefaultConfig()
	cfg.RootDir = filepath.Join(tempDir, "storage")
	cfg.TLSEnabled = false // Plain HTTP for tests
	if err := cfg.InitDirectories(); err != nil {
		t.Fatalf("InitDirectories failed: %v", err)
	}

	sessionMgr := NewSessionManager(cfg.GetSessionSecretBytes())
	loginRateLimiter := NewLoginRateLimiter(5, 5*time.Minute)
	taskMgr := NewTaskManager("yt-dlp", cfg.YtDir, 1, 10, "", "", "", nil)
	statsCollector := NewStatsCollector(cfg.RootDir)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", HandleServerStatus(cfg, sessionMgr))
	mux.HandleFunc("/api/setup", HandleSetup(cfg, sessionMgr, configPath))
	mux.HandleFunc("/api/login", HandleLogin(cfg, sessionMgr, loginRateLimiter, configPath))
	mux.HandleFunc("/api/logout", HandleLogout(cfg, sessionMgr))
	mux.HandleFunc("/api/files", HandleListFiles(cfg))
	mux.HandleFunc("/api/upload", HandleUpload(cfg))
	mux.HandleFunc("/api/download", HandleDownload(cfg))
	mux.HandleFunc("/api/delete", HandleDelete(cfg))
	mux.HandleFunc("/api/rename", HandleRename(cfg))
	mux.HandleFunc("/api/tasks", HandleTasks(taskMgr))
	mux.HandleFunc("/api/stats", HandleStats(statsCollector))

	var handler http.Handler = mux
	handler = AuthMiddleware(cfg, sessionMgr, handler)
	handler = CSRFMiddleware(false, handler)
	handler = SecurityHeadersMiddleware(false, handler)

	return cfg, sessionMgr, loginRateLimiter, taskMgr, statsCollector, handler, tempDir
}

func TestServerInitialStatusAndSetup(t *testing.T) {
	_, _, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	// 1. Initial status check: setup_needed should be true
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	var statusResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &statusResp)
	if statusResp["setup_needed"] != true {
		t.Errorf("Expected setup_needed: true, got %v", statusResp["setup_needed"])
	}

	// 2. Perform Setup: Set password
	setupPayload := `{"password":"AdminPassword123!","confirm_password":"AdminPassword123!"}`
	req = httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(setupPayload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Setup failed, expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify session cookie was set
	cookies := w.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == CookieSession {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Errorf("Expected session cookie to be set after setup")
	}

	// 3. Status check now: setup_needed should be false
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &statusResp)
	if statusResp["setup_needed"] != false {
		t.Errorf("Expected setup_needed: false after setup, got %v", statusResp["setup_needed"])
	}
}

func TestLoginAndAuthProtection(t *testing.T) {
	cfg, _, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	_ = cfg.SetPassword("SecretPassword123")

	// 1. Attempt to access protected endpoint without auth -> 401
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized, got %d", w.Code)
	}

	// 2. Login with incorrect password -> 401
	loginBad := `{"password":"WrongPassword"}`
	req = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(loginBad))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for bad password, got %d", w.Code)
	}

	// 3. Login with correct password -> 200
	loginGood := `{"password":"SecretPassword123"}`
	req = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(loginGood))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 for good password, got %d: %s", w.Code, w.Body.String())
	}

	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieSession {
			sessionCookie = c
		}
		if c.Name == CookieCSRF {
			csrfCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatalf("Missing session cookie")
	}

	// 4. Access protected endpoint with valid session cookie -> 200
	req = httptest.NewRequest(http.MethodGet, "/api/files", nil)
	req.AddCookie(sessionCookie)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for authenticated request, got %d", w.Code)
	}

	_ = csrfCookie
}

func TestFileUploadAndDeduplication(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)

	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	// Helper to send multipart upload
	uploadFile := func(filename, content string) *httptest.ResponseRecorder {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, _ := writer.CreateFormFile("file", filename)
		_, _ = io.WriteString(part, content)
		_ = writer.WriteField("path", "media/uploads")
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set(HeaderCSRF, csrfToken)
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
		req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// First upload: sample.txt
	w1 := uploadFile("sample.txt", "Initial content")
	if w1.Code != http.StatusOK {
		t.Fatalf("First upload failed: %d - %s", w1.Code, w1.Body.String())
	}

	// Second upload with identical filename: sample.txt
	w2 := uploadFile("sample.txt", "Second content")
	if w2.Code != http.StatusOK {
		t.Fatalf("Second upload failed: %d - %s", w2.Code, w2.Body.String())
	}

	// Check on disk: both should exist without overwrite!
	filePath1 := filepath.Join(cfg.UploadDir, "sample.txt")
	filePath2 := filepath.Join(cfg.UploadDir, "sample (1).txt")

	if _, err := os.Stat(filePath1); err != nil {
		t.Errorf("Original file was deleted or not found")
	}
	if _, err := os.Stat(filePath2); err != nil {
		t.Errorf("Deduplicated file was not created, possible overwrite occurred!")
	}
}

func TestVideoURLValidation(t *testing.T) {
	validURLs := []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"https://vimeo.com/123456789",
		"https://soundcloud.com/artist/track",
	}

	for _, u := range validURLs {
		if _, err := ValidateAndSanitizeVideoURL(u); err != nil {
			t.Errorf("Expected valid URL %s, got error: %v", u, err)
		}
	}

	// Exploit payloads that MUST be rejected
	maliciousURLs := []string{
		"--exec rm -rf /",
		"-o /etc/passwd",
		"file:///etc/shadow",
		"ftp://malicious.com/payload",
		"http://127.0.0.1:8080/admin",
		"http://localhost/admin",
		"https://192.168.1.1/router",
		"https://10.0.0.1/internal",
		"https://evil-unapproved-site.com/video",
	}

	for _, u := range maliciousURLs {
		if _, err := ValidateAndSanitizeVideoURL(u); err == nil {
			t.Errorf("Security check failed: malicious URL was allowed: %s", u)
		}
	}
}
