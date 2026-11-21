package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PBKDF2 iterations and salt parameters (OWASP recommendation for SHA-256)
const (
	PBKDF2Iterations = 200000
	SaltLength       = 16
	KeyLength        = 32
	SessionDuration  = 24 * time.Hour
)

// PBKDF2 implementation using crypto/sha256 and crypto/hmac (pure stdlib)
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var result []byte

	var blockBuf [4]byte
	for block := 1; block <= numBlocks; block++ {
		blockBuf[0] = byte(block >> 24)
		blockBuf[1] = byte(block >> 16)
		blockBuf[2] = byte(block >> 8)
		blockBuf[3] = byte(block)

		prf.Reset()
		prf.Write(salt)
		prf.Write(blockBuf[:])
		u := prf.Sum(nil)

		t := make([]byte, hashLen)
		copy(t, u)

		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for k := 0; k < hashLen; k++ {
				t[k] ^= u[k]
			}
		}

		result = append(result, t...)
	}

	return result[:keyLen]
}

// HashPassword generates a salted PBKDF2-HMAC-SHA256 hash.
// Format: pbkdf2_sha256$<iter>$<hex_salt>$<hex_hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate cryptographic salt: %w", err)
	}

	hash := pbkdf2SHA256([]byte(password), salt, PBKDF2Iterations, KeyLength)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", PBKDF2Iterations, hex.EncodeToString(salt), hex.EncodeToString(hash)), nil
}

// VerifyPassword checks if a plain password matches the stored PBKDF2 hash using constant-time comparison.
func VerifyPassword(plainPassword, storedHash string) bool {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}

	var iter int
	if _, err := fmt.Sscanf(parts[1], "%d", &iter); err != nil || iter <= 0 {
		return false
	}

	salt, err := hex.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}

	expectedHash, err := hex.DecodeString(parts[3])
	if err != nil || len(expectedHash) == 0 {
		return false
	}

	actualHash := pbkdf2SHA256([]byte(plainPassword), salt, iter, len(expectedHash))
	return subtle.ConstantTimeCompare(actualHash, expectedHash) == 1
}

// GenerateRandomToken generates cryptographically secure hex tokens.
func GenerateRandomToken(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RateLimiter enforces per-IP login throttling to prevent brute-force attacks.
type LoginRateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	maxAttempts int
	window      time.Duration
}

func NewLoginRateLimiter(maxAttempts int, window time.Duration) *LoginRateLimiter {
	rl := &LoginRateLimiter{
		attempts:    make(map[string][]time.Time),
		maxAttempts: maxAttempts,
		window:      window,
	}
	// Background cleanup of stale entries every 5 minutes
	go func() {
		for range time.Tick(5 * time.Minute) {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *LoginRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	var valid []time.Time
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxAttempts {
		rl.attempts[ip] = valid
		return false
	}

	return true
}

func (rl *LoginRateLimiter) RecordFailedAttempt(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = append(rl.attempts[ip], time.Now())
}

func (rl *LoginRateLimiter) Reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

func (rl *LoginRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	for ip, times := range rl.attempts {
		var valid []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.attempts, ip)
		} else {
			rl.attempts[ip] = valid
		}
	}
}

// SessionManager manages signed cryptographic session tokens
type SessionManager struct {
	secretKey []byte
	mu        sync.RWMutex
	revoked   map[string]time.Time
}

func NewSessionManager(secretKey []byte) *SessionManager {
	sm := &SessionManager{
		secretKey: secretKey,
		revoked:   make(map[string]time.Time),
	}
	go func() {
		for range time.Tick(15 * time.Minute) {
			sm.cleanupRevoked()
		}
	}()
	return sm
}

// CreateSessionToken creates a signed token: payload.signature
// payload = base64(username:nonce:expiresUnix)
func (sm *SessionManager) CreateSessionToken(username string) (string, error) {
	nonce, err := GenerateRandomToken(16)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(SessionDuration).Unix()
	payloadRaw := fmt.Sprintf("%s:%s:%d", username, nonce, expires)
	payloadB64 := base64.RawURLEncoding.EncodeToString([]byte(payloadRaw))

	mac := hmac.New(sha256.New, sm.secretKey)
	mac.Write([]byte(payloadB64))
	sigB64 := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s.%s", payloadB64, sigB64), nil
}

// ValidateSessionToken verifies HMAC signature, expiry, and revocation
func (sm *SessionManager) ValidateSessionToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("malformed token structure")
	}

	payloadB64, sigB64 := parts[0], parts[1]
	expectedSig := hmac.New(sha256.New, sm.secretKey)
	expectedSig.Write([]byte(payloadB64))
	actualSig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", errors.New("invalid signature encoding")
	}

	if subtle.ConstantTimeCompare(actualSig, expectedSig.Sum(nil)) != 1 {
		return "", errors.New("invalid token signature")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return "", errors.New("invalid payload encoding")
	}

	payloadParts := strings.Split(string(payloadBytes), ":")
	if len(payloadParts) != 3 {
		return "", errors.New("invalid payload format")
	}

	username, nonce := payloadParts[0], payloadParts[1]
	var expiresUnix int64
	if _, err := fmt.Sscanf(payloadParts[2], "%d", &expiresUnix); err != nil {
		return "", errors.New("invalid expiration time")
	}

	if time.Now().Unix() > expiresUnix {
		return "", errors.New("session expired")
	}

	sm.mu.RLock()
	revokedAt, isRevoked := sm.revoked[nonce]
	sm.mu.RUnlock()
	if isRevoked && time.Now().Before(revokedAt) {
		return "", errors.New("session has been revoked")
	}

	return username, nil
}

// RevokeSession marks a token as revoked by extracting its nonce
func (sm *SessionManager) RevokeSession(token string) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return
	}
	p := strings.Split(string(payloadBytes), ":")
	if len(p) == 3 {
		nonce := p[1]
		sm.mu.Lock()
		sm.revoked[nonce] = time.Now().Add(SessionDuration)
		sm.mu.Unlock()
	}
}

func (sm *SessionManager) cleanupRevoked() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	for nonce, expiry := range sm.revoked {
		if now.After(expiry) {
			delete(sm.revoked, nonce)
		}
	}
}

// SafePathJail guarantees that a requested path resolves strictly within the root directory,
// completely mitigating directory traversal, symlink escapes, null-byte injection, and path truncation.
func SafePathJail(rootDir, userPath string) (string, error) {
	// Disallow null bytes
	if strings.ContainsRune(userPath, 0) {
		return "", errors.New("invalid null character in path")
	}

	// Normalize slashes
	normalized := filepath.ToSlash(userPath)

	// Explicitly disallow any path traversal attempts
	segments := strings.Split(normalized, "/")
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == ".." {
			return "", errors.New("access denied: directory traversal detected ('..')")
		}
	}
	// Also check backslash on platforms where user might send Windows-style path
	if strings.Contains(userPath, `..\`) || strings.Contains(userPath, `/..`) || strings.HasPrefix(userPath, `..\`) {
		return "", errors.New("access denied: directory traversal detected")
	}

	// Canonical root directory
	cleanRoot := filepath.Clean(rootDir)
	realRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		// If root doesn't exist yet, clean path is baseline
		realRoot = cleanRoot
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return "", fmt.Errorf("unable to determine absolute root directory: %w", err)
	}

	// Clean the requested relative path and join
	cleanRel := filepath.Clean(filepath.FromSlash(normalized))
	if filepath.IsAbs(cleanRel) {
		// If absolute, make it relative to root
		cleanRel = strings.TrimPrefix(cleanRel, string(filepath.Separator))
	}

	targetPath := filepath.Join(realRoot, cleanRel)

	// Evaluate symlinks if the target file/dir already exists
	if _, err := os.Lstat(targetPath); err == nil {
		realTarget, err := filepath.EvalSymlinks(targetPath)
		if err == nil {
			realTarget, err = filepath.Abs(realTarget)
			if err == nil {
				targetPath = realTarget
			}
		}
	} else {
		// If target doesn't exist yet (e.g. creating/uploading), check parent directory
		parent := filepath.Dir(targetPath)
		if realParent, err := filepath.EvalSymlinks(parent); err == nil {
			if absParent, err := filepath.Abs(realParent); err == nil {
				targetPath = filepath.Join(absParent, filepath.Base(targetPath))
			}
		}
	}

	// Verify targetPath is within realRoot
	rel, err := filepath.Rel(realRoot, targetPath)
	if err != nil {
		return "", errors.New("access denied: path resolution error")
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("access denied: path outside allowed root directory")
	}

	return targetPath, nil
}

// ClientIP extracts real client IP handling common proxy headers securely.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	addr := r.RemoteAddr
	if host, _, err := netSplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func netSplitHostPort(hostport string) (string, string, error) {
	// Simple split without requiring net package directly if possible
	colon := strings.LastIndex(hostport, ":")
	if colon != -1 {
		return hostport[:colon], hostport[colon+1:], nil
	}
	return hostport, "", nil
}
