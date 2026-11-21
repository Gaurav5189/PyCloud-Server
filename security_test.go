package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	password := "SuperSecretPassword123!"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}

	if !VerifyPassword(password, hash) {
		t.Errorf("VerifyPassword failed for correct password")
	}

	if VerifyPassword("WrongPassword", hash) {
		t.Errorf("VerifyPassword succeeded for incorrect password")
	}

	if VerifyPassword(password, "invalid$format") {
		t.Errorf("VerifyPassword succeeded for malformed hash")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := NewLoginRateLimiter(3, 100*time.Millisecond)
	ip := "192.168.1.50"

	if !rl.Allow(ip) {
		t.Errorf("Initial request should be allowed")
	}
	rl.RecordFailedAttempt(ip)
	rl.RecordFailedAttempt(ip)
	rl.RecordFailedAttempt(ip)

	if rl.Allow(ip) {
		t.Errorf("Should block after 3 failed attempts")
	}

	time.Sleep(120 * time.Millisecond)
	if !rl.Allow(ip) {
		t.Errorf("Should allow after window expiration")
	}
}

func TestSessionManager(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	sm := NewSessionManager(secret)

	token, err := sm.CreateSessionToken("admin")
	if err != nil {
		t.Fatalf("CreateSessionToken failed: %v", err)
	}

	username, err := sm.ValidateSessionToken(token)
	if err != nil {
		t.Fatalf("ValidateSessionToken failed: %v", err)
	}
	if username != "admin" {
		t.Errorf("Expected username admin, got %s", username)
	}

	// Test tampering
	tampered := token[:len(token)-4] + "abcd"
	if _, err := sm.ValidateSessionToken(tampered); err == nil {
		t.Errorf("Tampered token should fail validation")
	}

	// Test revocation
	sm.RevokeSession(token)
	if _, err := sm.ValidateSessionToken(token); err == nil {
		t.Errorf("Revoked token should fail validation")
	}
}

func TestSafePathJail(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pycloud_jail_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Valid file inside jail
	subDir := filepath.Join(tempDir, "uploads")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subDir: %v", err)
	}

	testFile := filepath.Join(subDir, "document.pdf")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Test safe resolution
	safePath, err := SafePathJail(tempDir, "uploads/document.pdf")
	if err != nil {
		t.Errorf("Safe path rejected: %v", err)
	}
	if safePath != testFile {
		t.Errorf("Expected %s, got %s", testFile, safePath)
	}

	// Test traversal attempts
	traversalAttempts := []string{
		"../outside.txt",
		"../../../../etc/passwd",
		"uploads/../../etc/shadow",
		"..\\..\\windows\\system32",
		"uploads/../../../",
		"test\x00file.txt",
	}

	for _, attempt := range traversalAttempts {
		if _, err := SafePathJail(tempDir, attempt); err == nil {
			t.Errorf("Failed to block traversal attempt: %q", attempt)
		}
	}

	// Test symlink escape
	outsideDir, err := os.MkdirTemp("", "pycloud_outside_*")
	if err != nil {
		t.Fatalf("Failed to create outside temp dir: %v", err)
	}
	defer os.RemoveAll(outsideDir)

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0600)

	symlinkPath := filepath.Join(tempDir, "evil_symlink")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		if _, err := SafePathJail(tempDir, "evil_symlink"); err == nil {
			t.Errorf("SafePathJail failed to catch symlink pointing outside jail!")
		}
	}
}
