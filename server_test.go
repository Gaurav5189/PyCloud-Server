package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
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

// failingReader models a connection that dies mid-upload.
type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestUploadRejectsOversizedRequest(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	cfg.MaxUploadMB = 1

	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	body := bytes.NewReader(make([]byte, 2<<20)) // 2MB, above the 1MB test limit
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("Expected 413 for oversized request, got %d: %s", w.Code, w.Body.String())
	}
	var payload map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if !strings.Contains(payload["error"], "1 MB") {
		t.Errorf("Expected error to name the configured limit, got %q", payload["error"])
	}
}

func TestUploadChunkedOverLimitReturns413(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	cfg.MaxUploadMB = 1
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	const boundary = "chunkboundary"
	prefix := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"big.bin\"\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n"
	// io.MultiReader leaves ContentLength at -1, so the early header check is
	// skipped and MaxBytesReader has to catch the oversized body.
	body := io.MultiReader(strings.NewReader(prefix), bytes.NewReader(make([]byte, 2<<20)))

	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("Expected 413 for chunked oversized upload, got %d: %s", w.Code, w.Body.String())
	}
	var payload map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if !strings.Contains(payload["error"], "1 MB") {
		t.Errorf("Expected the configured limit in the error, got %q", payload["error"])
	}
}

func TestUploadStreamsToDestinationFolder(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("path", "Mob")
	part, err := writer.CreateFormFile("file", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "streamed content")
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/upload?path=Mob", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Streaming upload failed: %d - %s", w.Code, w.Body.String())
	}

	target := filepath.Join(cfg.RootDir, "Mob", "clip.mp4")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("Expected file in destination folder: %v", err)
	}
	if string(data) != "streamed content" {
		t.Errorf("Unexpected file content: %q", data)
	}

	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp_") {
			t.Errorf("Temporary upload file was not cleaned up: %s", entry.Name())
		}
	}
}

func TestUploadInterruptedIsNotReportedAsSizeLimit(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	const boundary = "testboundary"
	prefix := "--" + boundary + "\r\n" +
		"Content-Disposition: form-data; name=\"file\"; filename=\"clip.mp4\"\r\n" +
		"Content-Type: application/octet-stream\r\n\r\n" +
		strings.Repeat("x", 1024)
	body := io.MultiReader(strings.NewReader(prefix), failingReader{err: io.ErrUnexpectedEOF})

	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("Expected 400 for an interrupted upload, got %d: %s", w.Code, w.Body.String())
	}
	var payload map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if strings.Contains(payload["error"], "maximum limit") {
		t.Errorf("Interrupted upload was misreported as a size limit: %q", payload["error"])
	}
	if !strings.Contains(payload["error"], "interrupted") {
		t.Errorf("Expected an interruption message, got %q", payload["error"])
	}
}

func TestUploadStalledBodyReturns408(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	const idle = 200 * time.Millisecond
	srv := httptest.NewServer(BodyIdleTimeoutMiddleware(idle, handler))
	defer srv.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/upload", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	go func() {
		_ = mw.WriteField("path", "media/uploads")
		part, err := mw.CreateFormFile("file", "stall.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_, _ = part.Write([]byte("partial data"))
		time.Sleep(4 * idle)
		_ = pw.CloseWithError(errors.New("client gave up"))
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("Expected 408 for a stalled upload, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "timed out") {
		t.Errorf("Expected a timeout message, got %s", body)
	}
	if strings.Contains(string(body), "maximum limit") {
		t.Errorf("Stalled upload was misreported as a size limit: %s", body)
	}
}

func TestUploadErrorStatusClassification(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"size limit", &http.MaxBytesError{Limit: 1024}, http.StatusRequestEntityTooLarge},
		{"disk full", syscall.ENOSPC, http.StatusInsufficientStorage},
		{"read deadline", os.ErrDeadlineExceeded, http.StatusRequestTimeout},
		{"wrapped deadline", &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, http.StatusRequestTimeout},
		{"client reset", syscall.ECONNRESET, http.StatusBadRequest},
		{"unexpected eof", io.ErrUnexpectedEOF, http.StatusBadRequest},
		{"malformed", errors.New("multipart: NextPart: EOF"), http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, message := uploadErrorStatus(4096, tc.err, "receiving")
			if status != tc.status {
				t.Errorf("Expected status %d, got %d (%q)", tc.status, status, message)
			}
			if message == "" {
				t.Error("Expected a non-empty error message")
			}
		})
	}
}

func TestSanitizeUploadFilename(t *testing.T) {
	if got := sanitizeUploadFilename(`..\..\evil.txt`); strings.ContainsAny(got, `/\`) {
		t.Errorf("Path separators survived sanitisation: %q", got)
	}
	if got := sanitizeUploadFilename(""); !strings.HasPrefix(got, "upload_") {
		t.Errorf("Expected generated fallback name, got %q", got)
	}

	long := strings.Repeat("a", 400) + ".mp4"
	got := sanitizeUploadFilename(long)
	if len(got) > maxUploadNameBytes {
		t.Errorf("Name was not truncated: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Errorf("Extension was lost: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("Truncation split a UTF-8 rune: %q", got)
	}

	multiByte := strings.Repeat("é", 200) + ".mp4"
	got = sanitizeUploadFilename(multiByte)
	if !utf8.ValidString(got) {
		t.Errorf("Truncation split a multi-byte rune: %q", got)
	}
}

func TestServerDoesNotCapTotalRequestTime(t *testing.T) {
	srv := newHTTPServer(DefaultConfig(), http.NotFoundHandler(), nil)
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout must be 0 so large uploads are not cut off, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout must be 0 so large downloads are not cut off, got %v", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout must stay set for Slowloris protection")
	}
}

// TestSlowUploadSurvivesWhileProgressing guards the regression that broke a
// 150MB upload at ~28%: an upload may take longer than any fixed total timeout
// as long as it keeps sending data.
func TestSlowUploadSurvivesWhileProgressing(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	srv := httptest.NewServer(BodyIdleTimeoutMiddleware(150*time.Millisecond, handler))
	defer srv.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/upload", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(HeaderCSRF, csrfToken)
	req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
	req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

	const chunks = 10
	const chunkSize = 32 * 1024
	go func() {
		_ = mw.WriteField("path", "media/uploads")
		part, err := mw.CreateFormFile("file", "slow.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		chunk := make([]byte, chunkSize)
		for i := 0; i < chunks; i++ {
			if _, err := part.Write(chunk); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			// Progress every 50ms, comfortably inside the 150ms idle window.
			time.Sleep(50 * time.Millisecond)
		}
		_ = mw.Close()
		_ = pw.Close()
	}()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Slow upload failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected 200 for a progressing slow upload, got %d: %s", resp.StatusCode, body)
	}

	info, err := os.Stat(filepath.Join(cfg.UploadDir, "slow.bin"))
	if err != nil {
		t.Fatalf("Uploaded file missing: %v", err)
	}
	if info.Size() != chunks*chunkSize {
		t.Errorf("Expected %d bytes on disk, got %d", chunks*chunkSize, info.Size())
	}
}

func TestIsUploadTempName(t *testing.T) {
	cases := map[string]bool{
		".tmp_1700000000000000000_clip.mp4": true,
		".tmp_1_a":                          true,
		".tmp_backup.txt":                   false, // user file, no numeric prefix
		".tmp__x":                           false, // no digits
		".tmp_12":                           false, // no trailing underscore
		"tmp_12_a":                          false, // missing leading dot
		"clip.mp4":                          false,
	}
	for name, want := range cases {
		if got := isUploadTempName(name); got != want {
			t.Errorf("isUploadTempName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCleanupStaleUploadTemps(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	cutoff := time.Now().Add(-time.Hour)

	stale := filepath.Join(root, ".tmp_1700000000000000000_stale.mp4")
	recent := filepath.Join(root, ".tmp_1700000000000000001_recent.mp4")
	userTemp := filepath.Join(root, ".tmp_backup.txt")
	userFile := filepath.Join(root, "keep.txt")
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(sub, ".tmp_1700000000000000002_nested.mp4")

	for _, path := range []string{stale, recent, userTemp, userFile, nested} {
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{stale, userTemp, nested} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	cleanupStaleUploadTemps(root, cutoff)

	for _, path := range []string{stale, nested} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("stale upload temp %s was not removed", path)
		}
	}
	for _, path := range []string{recent, userTemp, userFile} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should have been kept: %v", path, err)
		}
	}
}

func TestCommitUploadProducesUniqueNames(t *testing.T) {
	dir := t.TempDir()
	for _, existing := range []string{"clip.mp4", "clip (1).mp4"} {
		if err := os.WriteFile(filepath.Join(dir, existing), []byte("existing"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	makeTemp := func(content string) string {
		path := filepath.Join(dir, fmt.Sprintf(".tmp_%d_x", time.Now().UnixNano()))
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	first, err := commitUpload(makeTemp("new content"), dir, "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if first != "clip (2).mp4" {
		t.Errorf("Expected clip (2).mp4, got %q", first)
	}

	second, err := commitUpload(makeTemp("newer content"), dir, "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if second != "clip (3).mp4" {
		t.Errorf("Expected clip (3).mp4, got %q", second)
	}

	data, err := os.ReadFile(filepath.Join(dir, first))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("Unexpected committed content: %q", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isUploadTempName(entry.Name()) {
			t.Errorf("Temporary file left behind: %s", entry.Name())
		}
	}
}

func TestUploadRejectsTrashDestination(t *testing.T) {
	cfg, sm, _, _, _, handler, tempDir := setupTestServer(t)
	defer os.RemoveAll(tempDir)
	_ = cfg.SetPassword("SecretPassword123")
	token, _ := sm.CreateSessionToken("admin")
	csrfToken, _ := GenerateRandomToken(32)

	send := func(target string) *httptest.ResponseRecorder {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("path", ".pycloud_trash")
		part, err := writer.CreateFormFile("file", "evil.mp4")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(part, "data")
		_ = writer.Close()

		req := httptest.NewRequest(http.MethodPost, target, body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set(HeaderCSRF, csrfToken)
		req.AddCookie(&http.Cookie{Name: CookieSession, Value: token})
		req.AddCookie(&http.Cookie{Name: CookieCSRF, Value: csrfToken})

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	for _, target := range []string{"/api/upload", "/api/upload?path=.pycloud_trash"} {
		w := send(target)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400 for a trash destination, got %d: %s", target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "read-only") {
			t.Errorf("%s: expected a read-only message, got %s", target, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(cfg.TrashDir, "evil.mp4")); !os.IsNotExist(err) {
			t.Errorf("%s: file was written into Trash", target)
		}
	}
}

// TestBodyIdleTimeoutMiddleware verifies that a stalled body is released while
// a body that keeps making progress is allowed to finish.
func TestBodyIdleTimeoutMiddleware(t *testing.T) {
	const idle = 250 * time.Millisecond
	handler := BodyIdleTimeoutMiddleware(idle, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				w.WriteHeader(http.StatusRequestTimeout)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, srv.URL, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	go func() {
		_, _ = pw.Write([]byte("hello"))
		time.Sleep(4 * idle)
		_ = pw.CloseWithError(errors.New("client gave up"))
	}()

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("Expected 408 from a stalled body, got %d", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed >= 4*idle {
		t.Errorf("Idle timeout did not fire early enough (%v)", elapsed)
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
