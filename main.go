package main

import (
	"context"
	"crypto/tls"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

//go:embed frontend/*
var embeddedFrontend embed.FS

const ServerVersion = "2.0.0"

// serverStartTime is captured once, at process start, so the UI can show how
// long the server itself has been running rather than how long this browser
// tab has been open.
var serverStartTime = time.Now()

// ServerUptimeSeconds returns the number of whole seconds the process has run.
func ServerUptimeSeconds() int64 {
	return int64(time.Since(serverStartTime).Seconds())
}

// resolveFFmpegPath prefers an explicit config path, then falls back to PATH.
// An empty result means yt-dlp cannot merge separate video and audio streams.
func resolveFFmpegPath(configured string) string {
	if configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured
		}
		log.Printf("⚠️  yt_dlp_ffmpeg_path is set to %q but is not a readable file", configured)
		return ""
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func main() {
	configPath := "config.json"
	if p := os.Getenv("PYCLOUD_CONFIG"); p != "" {
		configPath = p
	}

	// 1. Load Configuration
	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("❌ Failed to initialize configuration: %v", err)
	}

	// 2. Initialize Security & Core Components
	sessionMgr := NewSessionManager(cfg.GetSessionSecretBytes())
	loginRateLimiter := NewLoginRateLimiter(5, 5*time.Minute) // 5 attempts per 5 minutes

	// Resolve the optional media toolchain once so the startup banner can report
	// it and the task manager knows whether merging is possible.
	ffmpegPath := resolveFFmpegPath(cfg.YtDlpFfmpegPath)
	ytDlpInstalled, ytDlpPath := CheckYtDlpInstalled(cfg.YtDlpPath)
	taskMgr := NewTaskManager(cfg.YtDlpPath, cfg.YtDir, 2, 50, cfg.YtDlpFormat, cfg.YtDlpCookies, ffmpegPath, cfg.YtDlpExtraArgs)
	if cfg.YtDlpCookies != "" {
		if _, err := os.Stat(cfg.YtDlpCookies); err != nil {
			log.Printf("⚠️  yt_dlp_cookies is set to %q but the file is not readable: %v", cfg.YtDlpCookies, err)
		} else if f, err := os.OpenFile(cfg.YtDlpCookies, os.O_WRONLY, 0); err != nil {
			log.Printf("⚠️  yt_dlp_cookies %q is read-only: yt-dlp cannot save rotated cookies back, so they will expire sooner: %v", cfg.YtDlpCookies, err)
		} else {
			_ = f.Close()
		}
	}
	statsCollector := NewStatsCollector(cfg.RootDir)

	// 3. Setup Router & Routes
	mux := http.NewServeMux()

	// Extract embedded static filesystem
	frontendFS, err := fs.Sub(embeddedFrontend, "frontend")
	if err != nil {
		log.Fatalf("❌ Failed to read embedded assets: %v", err)
	}

	// Static Assets (/static/css/..., /static/js/...)
	fileServer := http.FileServer(http.FS(frontendFS))
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// Main Web Interface (SPA)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			// If not an API request, let the SPA handle client-side routes or 404
			http.NotFound(w, r)
			return
		}
		indexHTML, err := frontendFS.Open("index.html")
		if err != nil {
			http.Error(w, "Web interface asset missing", http.StatusInternalServerError)
			return
		}
		defer indexHTML.Close()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, indexHTML.(interface {
			Read([]byte) (int, error)
			Seek(int64, int) (int64, error)
		}))
	})

	// Public Auth & Status API
	mux.HandleFunc("/api/status", HandleServerStatus(cfg, sessionMgr))
	mux.HandleFunc("/api/setup", HandleSetup(cfg, sessionMgr, configPath))
	mux.HandleFunc("/api/login", HandleLogin(cfg, sessionMgr, loginRateLimiter, configPath))
	mux.HandleFunc("/api/logout", HandleLogout(cfg, sessionMgr))

	// Protected File Management API
	mux.HandleFunc("/api/files", HandleListFiles(cfg))
	mux.HandleFunc("/api/upload", HandleUpload(cfg))
	mux.HandleFunc("/api/download", HandleDownload(cfg))
	mux.HandleFunc("/api/delete", HandleDelete(cfg))
	mux.HandleFunc("/api/trash/empty", HandleEmptyTrash(cfg))
	mux.HandleFunc("/api/rename", HandleRename(cfg))
	mux.HandleFunc("/api/mkdir", HandleMkdir(cfg))

	// Protected Media Streaming API
	mux.HandleFunc("/api/media/stream", HandleMediaStream(cfg))
	mux.HandleFunc("/api/media/subtitles", HandleSubtitles(cfg))
	mux.HandleFunc("/api/media/info", HandleMediaInfo(cfg))

	// Protected Task Manager API
	mux.HandleFunc("/api/tasks", HandleTasks(taskMgr))
	mux.HandleFunc("/api/tasks/ytdlp-status", HandleYtDlpStatus(taskMgr))
	mux.HandleFunc("/api/tasks/install-ytdlp", HandleInstallYtDlp(taskMgr))

	// Protected System Telemetry API
	mux.HandleFunc("/api/stats", HandleStats(statsCollector))

	// 4. Assemble Middleware Pipeline
	var handler http.Handler = mux
	handler = AuthMiddleware(cfg, sessionMgr, handler)
	handler = CSRFMiddleware(cfg.TLSEnabled, handler)
	handler = SecurityHeadersMiddleware(cfg.TLSEnabled, handler)
	handler = LoggingMiddleware(handler)

	// 5. TLS Configuration & Certificate Provisioning
	var tlsConfig *tls.Config
	if cfg.TLSEnabled {
		if err := EnsureTLSCertificates(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
			log.Fatalf("❌ Failed to provision TLS certificates: %v", err)
		}

		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		}
	}

	// 6. HTTP Server Setup with Resilient Timeouts (Slowloris Protection)
	listenAddr := fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // Adequate for streaming & downloads
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	// 7. Print Startup Banner
	proto := "http"
	if cfg.TLSEnabled {
		proto = "https"
	}

	fmt.Println()
	fmt.Println("==================================================================")
	fmt.Printf(" ☁️  PyCloud v%s - Secure Self-Hosted Personal Cloud\n", ServerVersion)
	fmt.Println("==================================================================")
	fmt.Printf(" 🌐 Address:      %s://%s\n", proto, listenAddr)
	if cfg.BindAddr == "0.0.0.0" {
		fmt.Printf(" 🏠 Local:        %s://localhost:%d\n", proto, cfg.Port)
		if localIP := getOutboundIP(); localIP != "" {
			fmt.Printf(" 📡 Network:      %s://%s:%d\n", proto, localIP, cfg.Port)
		}
	}
	fmt.Printf(" 📁 Storage Root: %s\n", cfg.RootDir)
	fmt.Printf(" 🔒 Encryption:   %s (TLS 1.2+)\n", map[bool]string{true: "Enabled", false: "Disabled"}[cfg.TLSEnabled])
	if !cfg.IsPasswordSet() {
		fmt.Println(" ⚠️  Status:       Setup Required (open browser to set admin password)")
	} else {
		fmt.Println(" 🛡️  Status:       Protected (password authenticated)")
	}
	if ytDlpInstalled {
		fmt.Printf(" 🎬 yt-dlp:       %s\n", ytDlpPath)
	} else {
		fmt.Println(" ⚠️  yt-dlp:       not found — Fetch media is unavailable")
	}
	if ffmpegPath != "" {
		fmt.Printf(" 🎞️  ffmpeg:       %s\n", ffmpegPath)
	} else {
		fmt.Println(" ⚠️  ffmpeg:       not found — video+audio merging is unavailable")
	}
	if !ytDlpInstalled || ffmpegPath == "" {
		fmt.Println("    Install the missing tool(s) to enable downloads — see README → Troubleshooting.")
	}
	fmt.Println("==================================================================")
	fmt.Println(" Server is ready. Press Ctrl+C to terminate.")
	fmt.Println()

	// 8. Server Execution & Graceful Shutdown
	serverErr := make(chan error, 1)
	go func() {
		if cfg.TLSEnabled {
			serverErr <- server.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			serverErr <- server.ListenAndServe()
		}
	}()

	// Listen for OS interrupt signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("❌ Server failed to start: %v", err)
		}
	case sig := <-quit:
		log.Printf("Received signal %s. Initiating graceful shutdown...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			log.Fatalf("❌ Server shutdown error: %v", err)
		}
		log.Println("✅ PyCloud server stopped gracefully.")
	}
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
