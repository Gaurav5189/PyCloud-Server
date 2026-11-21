package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Emptying Trash must remove its entries (files and nested folders) and must
// never touch anything outside the trash directory.
func TestHandleEmptyTrashRemovesEntriesOnly(t *testing.T) {
	root := t.TempDir()
	trash := filepath.Join(root, ".pycloud_trash")
	if err := os.MkdirAll(filepath.Join(trash, "2_folder"), 0o755); err != nil {
		t.Fatalf("mkdir trash folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trash, "1_file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write trash file: %v", err)
	}
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write kept file: %v", err)
	}

	cfg := &Config{RootDir: root, TrashDir: trash}
	rec := httptest.NewRecorder()
	HandleEmptyTrash(cfg)(rec, httptest.NewRequest(http.MethodPost, "/api/trash/empty", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2", body.Deleted)
	}

	entries, err := os.ReadDir(trash)
	if err != nil {
		t.Fatalf("read trash: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("trash still contains %d entries", len(entries))
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("file outside trash was removed: %v", err)
	}
}

func TestHandleEmptyTrashRejectsGet(t *testing.T) {
	cfg := &Config{RootDir: t.TempDir(), TrashDir: filepath.Join(t.TempDir(), ".pycloud_trash")}
	rec := httptest.NewRecorder()
	HandleEmptyTrash(cfg)(rec, httptest.NewRequest(http.MethodGet, "/api/trash/empty", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
