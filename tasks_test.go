package main

import (
	"os"
	"path/filepath"
	"runtime"
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

	tm := NewTaskManager(stub, dir, 1, 4)
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

	tm := NewTaskManager(stub, dir, 1, 4)
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
