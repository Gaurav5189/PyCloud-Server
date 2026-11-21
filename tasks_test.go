package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeStubYtDlp writes a fake yt-dlp executable we can drive without network.
func writeStubYtDlp(t *testing.T, dir, script string) string {
	t.Helper()
	path := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub yt-dlp: %v", err)
	}
	return path
}

// getTasksBounded runs GetTasks in a goroutine so a regression that deadlocks
// the lock fails the test instead of hanging the whole suite.
func getTasksBounded(t *testing.T, tm *TaskManager) []*Task {
	t.Helper()
	done := make(chan []*Task, 1)
	go func() { done <- tm.GetTasks() }()
	select {
	case tasks := <-done:
		return tasks
	case <-time.After(3 * time.Second):
		t.Fatal("GetTasks blocked: task manager deadlocked (nested task.mu lock?)")
		return nil
	}
}

// A finished job must release its lock. Previously worker() called AddLog while
// holding task.mu, deadlocking the worker and every subsequent /api/tasks call.
func TestTaskCompletionDoesNotDeadlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub")
	}
	dir := t.TempDir()
	stub := writeStubYtDlp(t, dir,
		"#!/bin/sh\necho '[download] Destination: stub.mp4'\necho '[download] 100% of 1.00MiB'\nexit 0\n")

	tm := NewTaskManager(stub, dir, 1, 4, "", "", "", nil)
	id, err := tm.AddDownloadTask("Media Download", "https://www.youtube.com/watch?v=abc123")
	if err != nil {
		t.Fatalf("AddDownloadTask: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range getTasksBounded(t, tm) {
			if task.ID == id && task.Status == StatusCompleted {
				if len(task.Logs) < 2 {
					t.Fatalf("expected the job's log output to be kept, got %d lines", len(task.Logs))
				}
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("task never reached completed")
}

// Cancelling used to take task.mu and then call AddLog, which re-locks task.mu.
func TestCancelTaskDoesNotDeadlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub")
	}
	dir := t.TempDir()
	stub := writeStubYtDlp(t, dir, "#!/bin/sh\necho started\nsleep 5\n")

	tm := NewTaskManager(stub, dir, 1, 4, "", "", "", nil)
	id, err := tm.AddDownloadTask("Media Download", "https://youtu.be/abc")
	if err != nil {
		t.Fatalf("AddDownloadTask: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	running := false
	for time.Now().Before(deadline) && !running {
		for _, task := range getTasksBounded(t, tm) {
			if task.ID == id && task.Status == StatusRunning {
				running = true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !running {
		t.Fatal("task never started running")
	}

	done := make(chan bool, 1)
	go func() { done <- tm.CancelTask(id) }()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("CancelTask reported the task was not found")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CancelTask blocked: nested task.mu lock?")
	}

	getTasksBounded(t, tm) // must still respond after cancelling
}

// Format, cookies and extra flags must be injected, and the URL must remain the
// final argument after "--" so nothing operator-supplied can be read as a URL.
func TestYtDlpCommandBuildsArgs(t *testing.T) {
	dir := t.TempDir()
	tm := NewTaskManager("yt-dlp", dir, 1, 1, DefaultYtDlpFormat, "/etc/pycloud/cookies.txt", "/usr/bin/ffmpeg",
		[]string{"--js-runtimes", "deno", "  ", "--", "--proxy", "http://127.0.0.1:8080"})

	args := tm.ytDlpCommand("/usr/bin/yt-dlp", "https://youtu.be/abc")

	want := []string{
		"/usr/bin/yt-dlp",
		"--no-exec", "--no-config", "--restrict-filenames", "--no-playlist",
		"-f", DefaultYtDlpFormat,
		"--cookies", "/etc/pycloud/cookies.txt",
		"--ffmpeg-location", "/usr/bin/ffmpeg",
		"--js-runtimes", "deno",
		"--proxy", "http://127.0.0.1:8080",
		"-o", filepath.Join(dir, "%(title)s.%(ext)s"),
		"--", "https://youtu.be/abc",
	}
	if len(args) != len(want) {
		t.Fatalf("arg count: got %d %q, want %d %q", len(args), args, len(want), want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("arg %d: got %q want %q\nfull argv: %q", i, args[i], want[i], args)
		}
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "https://youtu.be/abc" {
		t.Fatalf("URL must terminate the argv after --: %q", args)
	}
	if strings.Contains(strings.Join(args, " "), "--   --") {
		t.Fatalf("stray empty extra arg leaked into argv: %q", args)
	}
}

func TestYtDlpCommandOmitsCookiesWhenUnset(t *testing.T) {
	tm := NewTaskManager("yt-dlp", t.TempDir(), 1, 1, "", "", "", nil)
	for _, arg := range tm.ytDlpCommand("yt-dlp", "https://youtu.be/abc") {
		if arg == "--cookies" {
			t.Fatal("--cookies must not be added when no cookies file is configured")
		}
	}
}

// An empty yt_dlp_format must mean "use yt-dlp's own choice", not an empty -f.
func TestYtDlpCommandOmitsFormatWhenEmpty(t *testing.T) {
	tm := NewTaskManager("yt-dlp", t.TempDir(), 1, 1, "", "", "", nil)
	for _, arg := range tm.ytDlpCommand("yt-dlp", "https://youtu.be/abc") {
		if arg == "-f" || strings.HasPrefix(arg, "--format") {
			t.Fatalf("no format flag expected when yt_dlp_format is empty: %q", arg)
		}
	}
}

// If the operator pins their own format in yt_dlp_extra_args, ours must be
// dropped: two -f flags make yt-dlp fail with "Requested format is not available".
func TestYtDlpCommandDefersToOperatorFormat(t *testing.T) {
	cases := [][]string{
		{"-f", "worst"},
		{"-fworst"},
		{"--format", "worst"},
		{"--format=worst"},
	}
	for _, extra := range cases {
		tm := NewTaskManager("yt-dlp", t.TempDir(), 1, 1, DefaultYtDlpFormat, "", "", extra)
		args := tm.ytDlpCommand("yt-dlp", "https://youtu.be/abc")

		count := 0
		for _, arg := range args {
			if arg == DefaultYtDlpFormat {
				t.Fatalf("built-in format leaked alongside operator format %q: %q", extra, args)
			}
			if arg == "-f" || arg == "--format" || strings.HasPrefix(arg, "--format=") || (strings.HasPrefix(arg, "-f") && len(arg) > 2) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one format flag for extra %q, got %d: %q", extra, count, args)
		}
	}
}

// A merge selector needs ffmpeg. Without it yt-dlp only warns and leaves two
// files: a silent video-only stream plus a detached audio stream.
func TestAddDownloadTaskRequiresFFmpegForMergedFormats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub")
	}
	dir := t.TempDir()
	stub := writeStubYtDlp(t, dir, "#!/bin/sh\nexit 0\n")
	tm := NewTaskManager(stub, dir, 1, 1, DefaultYtDlpFormat, "", "", nil)

	_, err := tm.AddDownloadTask("Media Download", "https://youtu.be/abc")
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("expected an ffmpeg error, got %v", err)
	}
}

func TestAddDownloadTaskAllowsSingleFileWithoutFFmpeg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub")
	}
	dir := t.TempDir()
	stub := writeStubYtDlp(t, dir, "#!/bin/sh\nexit 0\n")
	tm := NewTaskManager(stub, dir, 1, 1, "b[ext=mp4]/b", "", "", nil)

	if _, err := tm.AddDownloadTask("Media Download", "https://youtu.be/abc"); err != nil {
		t.Fatalf("a single-file format must not require ffmpeg: %v", err)
	}
}

func TestYtDlpCommandOmitsFFmpegLocationWhenUnset(t *testing.T) {
	tm := NewTaskManager("yt-dlp", t.TempDir(), 1, 1, "b", "", "", nil)
	for _, arg := range tm.ytDlpCommand("yt-dlp", "https://youtu.be/abc") {
		if arg == "--ffmpeg-location" {
			t.Fatal("--ffmpeg-location must be omitted when no ffmpeg is configured")
		}
	}
}

func TestResolveFFmpegPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	if got := resolveFFmpegPath(bin); got != bin {
		t.Fatalf("configured ffmpeg: got %q, want %q", got, bin)
	}
	if got := resolveFFmpegPath(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing configured ffmpeg: got %q, want empty", got)
	}
}
