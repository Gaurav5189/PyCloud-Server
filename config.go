package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Port               int    `json:"port"`
	BindAddr           string `json:"bind_addr"`
	RootDir            string `json:"root_dir"`
	PasswordHash       string `json:"password_hash"`
	SessionSecret      string `json:"session_secret"`
	TLSEnabled         bool   `json:"tls_enabled"`
	TLSCertFile        string `json:"tls_cert_file"`
	TLSKeyFile         string `json:"tls_key_file"`
	MaxUploadMB        int64  `json:"max_upload_mb"`
	TrashRetentionDays int    `json:"trash_retention_days"`
	YtDlpPath          string `json:"yt_dlp_path"`

	// Derived paths
	MediaDir  string `json:"-"`
	UploadDir string `json:"-"`
	YtDir     string `json:"-"`
	TrashDir  string `json:"-"`
}

func DefaultConfig() *Config {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	secret, _ := GenerateRandomToken(32)

	return &Config{
		Port:               8443,
		BindAddr:           "0.0.0.0",
		RootDir:            filepath.Join(cwd, "storage"),
		PasswordHash:       "",
		SessionSecret:      secret,
		TLSEnabled:         true,
		TLSCertFile:        filepath.Join(cwd, "certs", "server.crt"),
		TLSKeyFile:         filepath.Join(cwd, "certs", "server.key"),
		MaxUploadMB:        4096, // 4GB
		TrashRetentionDays: 30,
		YtDlpPath:          "yt-dlp",
	}
}

// InitDirectories initializes the root directory structure
func (c *Config) InitDirectories() error {
	absRoot, err := filepath.Abs(c.RootDir)
	if err != nil {
		return fmt.Errorf("invalid root directory path: %w", err)
	}
	c.RootDir = absRoot

	c.MediaDir = filepath.Join(c.RootDir, "media")
	c.UploadDir = filepath.Join(c.MediaDir, "uploads")
	c.YtDir = filepath.Join(c.MediaDir, "yt_library")
	c.TrashDir = filepath.Join(c.RootDir, ".pycloud_trash")

	dirs := []string{c.RootDir, c.MediaDir, c.UploadDir, c.YtDir, c.TrashDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}
	return nil
}

// LoadConfig loads configuration from a JSON file, ENV vars, and CLI flags
func LoadConfig(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	// 1. Load from file if exists
	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
		}
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config JSON: %w", err)
		}
	}

	// 2. Override with ENV variables
	if v := os.Getenv("PYCLOUD_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("PYCLOUD_BIND"); v != "" {
		cfg.BindAddr = v
	}
	if v := os.Getenv("PYCLOUD_ROOT"); v != "" {
		cfg.RootDir = v
	}
	if v := os.Getenv("PYCLOUD_TLS"); v != "" {
		cfg.TLSEnabled = (v == "true" || v == "1")
	}
	if v := os.Getenv("PYCLOUD_PASSWORD"); v != "" {
		hash, err := HashPassword(v)
		if err == nil {
			cfg.PasswordHash = hash
		}
	}

	// 3. Command Line Flags
	var (
		flagPort     = flag.Int("port", 0, "HTTP/HTTPS port to listen on")
		flagBind     = flag.String("bind", "", "IP interface to bind to (e.g. 127.0.0.1 or 0.0.0.0)")
		flagRoot     = flag.String("root", "", "Root storage directory")
		flagNoTLS    = flag.Bool("no-tls", false, "Disable TLS/HTTPS (run plain HTTP)")
		flagSetPass  = flag.String("set-password", "", "Set a new admin password and exit")
		flagCertFile = flag.String("cert", "", "Path to TLS certificate file")
		flagKeyFile  = flag.String("key", "", "Path to TLS private key file")
	)

	// If flags have not already been parsed in main
	if !flag.Parsed() {
		flag.Parse()
	}

	if *flagPort > 0 {
		cfg.Port = *flagPort
	}
	if *flagBind != "" {
		cfg.BindAddr = *flagBind
	}
	if *flagRoot != "" {
		cfg.RootDir = *flagRoot
	}
	if *flagNoTLS {
		cfg.TLSEnabled = false
	}
	if *flagCertFile != "" {
		cfg.TLSCertFile = *flagCertFile
	}
	if *flagKeyFile != "" {
		cfg.TLSKeyFile = *flagKeyFile
	}

	if *flagSetPass != "" {
		hash, err := HashPassword(*flagSetPass)
		if err != nil {
			return nil, fmt.Errorf("failed to hash password: %w", err)
		}
		cfg.PasswordHash = hash
		if err := cfg.Save(configPath); err != nil {
			return nil, fmt.Errorf("failed to save config with new password: %w", err)
		}
		fmt.Println("✅ Admin password successfully updated in config!")
		os.Exit(0)
	}

	// Ensure SessionSecret is valid
	if cfg.SessionSecret == "" {
		secret, _ := GenerateRandomToken(32)
		cfg.SessionSecret = secret
	}

	if err := cfg.InitDirectories(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save writes configuration to file atomically
func (c *Config) Save(configPath string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := configPath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpFile, configPath)
}

// GetSessionSecretBytes decodes hex secret to raw bytes
func (c *Config) GetSessionSecretBytes() []byte {
	b, err := hex.DecodeString(c.SessionSecret)
	if err == nil && len(b) >= 32 {
		return b
	}
	return []byte(c.SessionSecret)
}

// IsPasswordSet checks if admin password has been established
func (c *Config) IsPasswordSet() bool {
	return len(c.PasswordHash) > 0
}

// SetPassword hashes and updates the password
func (c *Config) SetPassword(newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters long")
	}
	h, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	c.PasswordHash = h
	return nil
}
