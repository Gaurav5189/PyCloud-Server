package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type TaskStatus string

const (
	StatusWaiting   TaskStatus = "waiting"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
)

type Task struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Target    string     `json:"target"`
	Status    TaskStatus `json:"status"`
	Logs      []string   `json:"logs"`
	CreatedAt int64      `json:"created_at"`

	mu         sync.Mutex         `json:"-"`
	cancelFunc context.CancelFunc `json:"-"`
	cmdArgs    []string           `json:"-"`
}

func (t *Task) AddLog(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	t.Logs = append(t.Logs, entry)
	if len(t.Logs) > 50 {
		t.Logs = t.Logs[len(t.Logs)-50:]
	}
}

// setFailed marks the task as failed and releases its cancel function. Call it
// without holding task.mu.
func (t *Task) setFailed(cancel context.CancelFunc) {
	t.mu.Lock()
	t.Status = StatusFailed
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type TaskManager struct {
	mu         sync.RWMutex
	tasks      []*Task
	queue      chan *Task
	maxHistory int
	ytDlpPath  string
	outputDir  string
}

func NewTaskManager(ytDlpPath, outputDir string, maxWorkers, queueSize int) *TaskManager {
	tm := &TaskManager{
		tasks:      make([]*Task, 0),
		queue:      make(chan *Task, queueSize),
		maxHistory: 100,
		ytDlpPath:  ytDlpPath,
		outputDir:  outputDir,
	}

	// Start worker pool
	for i := 0; i < maxWorkers; i++ {
		go tm.worker()
	}

	return tm
}

func (tm *TaskManager) worker() {
	for task := range tm.queue {
		task.mu.Lock()
		cancelled := task.Status == StatusCancelled
		task.mu.Unlock()
		if cancelled {
			continue
		}

		// 2-hour timeout context for execution
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)

		task.mu.Lock()
		task.Status = StatusRunning
		task.cancelFunc = cancel
		task.mu.Unlock()

		task.AddLog(fmt.Sprintf("Starting %s download...", task.Type))

		cmd := exec.CommandContext(ctx, task.cmdArgs[0], task.cmdArgs[1:]...)

		// Capture stdout and stderr merged
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			task.setFailed(cancel)
			task.AddLog(fmt.Sprintf("Failed to initialize stdout pipe: %v", err))
			continue
		}
		cmd.Stderr = cmd.Stdout

		if err := cmd.Start(); err != nil {
			task.setFailed(cancel)
			task.AddLog(fmt.Sprintf("Error starting process: %v", err))
			continue
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				task.AddLog(line)
			}
		}

		waitErr := cmd.Wait()
		cancel()

		// IMPORTANT: never hold task.mu while calling task.AddLog — AddLog takes
		// the same lock, and sync.Mutex is not reentrant (this used to deadlock
		// the worker, and with it every /api/tasks request, the moment a job
		// finished).
		task.mu.Lock()
		if task.Status != StatusCancelled {
			if waitErr == nil {
				task.Status = StatusCompleted
			} else {
				task.Status = StatusFailed
			}
		}
		finished := task.Status
		task.mu.Unlock()

		if finished == StatusCompleted {
			task.AddLog("Task completed successfully.")
		} else if finished == StatusFailed {
			task.AddLog(fmt.Sprintf("Process exited with error: %v", waitErr))
		}
	}
}

// ValidateAndSanitizeVideoURL ensures URL is safe, HTTPS, from known domains, and not targeting private IP/SSRF
func ValidateAndSanitizeVideoURL(rawURL string) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "-") {
		return nil, errors.New("invalid URL: argument injection attempt detected")
	}

	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("invalid URL structure")
	}

	if u.Scheme != "https" {
		return nil, errors.New("only secure HTTPS video links are permitted")
	}

	host := strings.ToLower(u.Hostname())

	// Reject localhost and IP literals
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil, errors.New("access denied: internal localhost addresses are blocked")
	}

	// Check if host is an IP address
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return nil, errors.New("access denied: private and internal IP ranges are blocked")
		}
	}

	// Whitelist of allowed video platforms
	allowedDomains := []string{
		"youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be",
		"vimeo.com", "www.vimeo.com",
		"dailymotion.com", "www.dailymotion.com",
		"soundcloud.com", "www.soundcloud.com",
		"twitch.tv", "www.twitch.tv",
		"twitter.com", "x.com",
		"instagram.com", "www.instagram.com",
		"reddit.com", "www.reddit.com",
		"tiktok.com", "www.tiktok.com",
	}

	matched := false
	for _, domain := range allowedDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			matched = true
			break
		}
	}

	if !matched {
		return nil, fmt.Errorf("domain %s is not in the allowed video provider list", host)
	}

	return u, nil
}

// CheckYtDlpInstalled checks if yt-dlp is present on PATH, current directory, or next to executable
func CheckYtDlpInstalled(customPath string) (bool, string) {
	if customPath != "" {
		if p, err := exec.LookPath(customPath); err == nil {
			return true, p
		}
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return true, p
	}

	candidates := []string{
		"./yt-dlp",
		"./yt-dlp.exe",
		"./yt-dlp_linux",
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "yt-dlp"),
			filepath.Join(dir, "yt-dlp.exe"),
			filepath.Join(dir, "yt-dlp_linux"),
		)
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			abs, _ := filepath.Abs(c)
			return true, abs
		}
	}

	return false, ""
}

// DownloadOfficialYtDlp downloads the official standalone executable from GitHub release
func DownloadOfficialYtDlp() (string, error) {
	var downloadURL string
	targetName := "yt-dlp"

	switch runtime.GOOS {
	case "windows":
		downloadURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp.exe"
		targetName = "yt-dlp.exe"
	case "darwin":
		downloadURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_macos"
	case "linux":
		if runtime.GOARCH == "arm64" {
			downloadURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux_aarch64"
		} else {
			downloadURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp_linux"
		}
	default:
		return "", errors.New("unsupported platform for automatic yt-dlp download")
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("network error downloading yt-dlp: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub release returned HTTP status %d", resp.StatusCode)
	}

	tmpFile := targetName + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to create destination file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	_ = f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpFile)
		return "", fmt.Errorf("download interrupted: %w", copyErr)
	}

	if err := os.Rename(tmpFile, targetName); err != nil {
		_ = os.Remove(tmpFile)
		return "", fmt.Errorf("failed to finalize binary: %w", err)
	}

	_ = os.Chmod(targetName, 0755)
	absPath, _ := filepath.Abs(targetName)
	return absPath, nil
}

// AddDownloadTask queues a safe yt-dlp download task
func (tm *TaskManager) AddDownloadTask(jobType, videoURL string) (string, error) {
	parsedURL, err := ValidateAndSanitizeVideoURL(videoURL)
	if err != nil {
		return "", err
	}

	// Verify yt-dlp executable exists
	installed, execPath := CheckYtDlpInstalled(tm.ytDlpPath)
	if !installed {
		return "", errors.New("ytdlp_missing: yt-dlp is not installed on this system")
	}

	// Generate UUID v4
	idBytes := make([]byte, 16)
	_, _ = rand.Read(idBytes)
	idBytes[6] = (idBytes[6] & 0x0f) | 0x40
	idBytes[8] = (idBytes[8] & 0x3f) | 0x80
	taskID := hex.EncodeToString(idBytes)

	outputTemplate := filepath.Join(tm.outputDir, "%(title)s.%(ext)s")

	// Hardened yt-dlp arguments
	args := []string{
		execPath,
		"--no-exec",            // Prevent command execution via yt-dlp configs
		"--no-config",          // Ignore system configuration files
		"--restrict-filenames", // Prevent dangerous characters in filenames
		"--no-playlist",        // Prevent downloading entire multi-gigabyte playlists
		"-o", outputTemplate,
		"--", // End of command flags - URL follows
		parsedURL.String(),
	}

	task := &Task{
		ID:        taskID,
		Type:      jobType,
		Target:    parsedURL.String(),
		Status:    StatusWaiting,
		Logs:      []string{fmt.Sprintf("[%s] Task queued", time.Now().Format("15:04:05"))},
		CreatedAt: time.Now().Unix(),
		cmdArgs:   args,
	}

	tm.mu.Lock()
	tm.tasks = append([]*Task{task}, tm.tasks...)
	// Limit history to maxHistory
	if len(tm.tasks) > tm.maxHistory {
		tm.tasks = tm.tasks[:tm.maxHistory]
	}
	tm.mu.Unlock()

	select {
	case tm.queue <- task:
		return taskID, nil
	default:
		task.setFailed(nil)
		task.AddLog("Download queue is full. Try again later.")
		return "", errors.New("task queue is full")
	}
}

// CancelTask cancels a running or queued task
func (tm *TaskManager) CancelTask(id string) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for _, t := range tm.tasks {
		if t.ID != id {
			continue
		}

		t.mu.Lock()
		cancelFn := t.cancelFunc
		changed := false
		if t.Status == StatusRunning && cancelFn != nil {
			t.Status = StatusCancelled
			changed = true
		} else if t.Status == StatusWaiting {
			t.Status = StatusCancelled
			changed = true
		}
		t.mu.Unlock()

		// Called outside the lock; AddLog would otherwise deadlock on t.mu.
		if cancelFn != nil {
			cancelFn()
		}
		if changed {
			t.AddLog("Cancelled by user.")
		}
		return true
	}
	return false
}

// RetryTask re-queues a failed or cancelled task
func (tm *TaskManager) RetryTask(id string) (string, error) {
	tm.mu.RLock()
	var targetTask *Task
	for _, t := range tm.tasks {
		if t.ID == id {
			targetTask = t
			break
		}
	}
	tm.mu.RUnlock()

	if targetTask == nil {
		return "", errors.New("task not found")
	}

	return tm.AddDownloadTask(targetTask.Type, targetTask.Target)
}

// GetTasksJSON returns a thread-safe snapshot of all tasks
func (tm *TaskManager) GetTasks() []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	copyList := make([]*Task, len(tm.tasks))
	for i, t := range tm.tasks {
		t.mu.Lock()
		logsCopy := make([]string, len(t.Logs))
		copy(logsCopy, t.Logs)
		copyList[i] = &Task{
			ID:        t.ID,
			Type:      t.Type,
			Target:    t.Target,
			Status:    t.Status,
			Logs:      logsCopy,
			CreatedAt: t.CreatedAt,
		}
		t.mu.Unlock()
	}
	return copyList
}

// HandleYtDlpStatus returns whether yt-dlp is available on the host
func HandleYtDlpStatus(tm *TaskManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		installed, execPath := CheckYtDlpInstalled(tm.ytDlpPath)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"installed": installed,
			"path":      execPath,
		})
	}
}

// HandleInstallYtDlp downloads and configures yt-dlp upon user confirmation
func HandleInstallYtDlp(tm *TaskManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		path, err := DownloadOfficialYtDlp()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "error",
				"error":  err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "yt-dlp downloaded and configured successfully",
			"path":    path,
		})
	}
}

// Handlers for task endpoints
func HandleTasks(tm *TaskManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodGet:
			tasks := tm.GetTasks()
			_ = json.NewEncoder(w).Encode(tasks)

		case http.MethodPost:
			var req struct {
				Action string `json:"action"` // "add", "cancel", "retry"
				URL    string `json:"url"`
				TaskID string `json:"task_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"Invalid JSON payload"}`, http.StatusBadRequest)
				return
			}

			switch req.Action {
			case "add":
				id, err := tm.AddDownloadTask("Media Download", req.URL)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					errMsg := err.Error()
					isMissing := strings.Contains(errMsg, "ytdlp_missing")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error":         errMsg,
						"ytdlp_missing": isMissing,
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued", "task_id": id})

			case "cancel":
				ok := tm.CancelTask(req.TaskID)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "found": ok})

			case "retry":
				newID, err := tm.RetryTask(req.TaskID)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued", "task_id": newID})

			default:
				http.Error(w, `{"error":"Unknown task action"}`, http.StatusBadRequest)
			}

		default:
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}
