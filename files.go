package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type FileItem struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	HumanSize string `json:"human_size"`
	ModTime   int64  `json:"mod_time"`
	Type      string `json:"type"` // "video", "audio", "image", "document", "other", "folder"
	Ext       string `json:"ext"`
}

func FormatHumanSize(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}

func DetectFileType(ext string, isDir bool) string {
	if isDir {
		return "folder"
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	switch ext {
	case "mp4", "mkv", "webm", "mov", "avi", "flv", "m4v":
		return "video"
	case "mp3", "wav", "flac", "aac", "ogg", "m4a", "opus":
		return "audio"
	case "png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico":
		return "image"
	case "pdf", "txt", "md", "docx", "xlsx", "pptx", "csv", "json", "yaml", "xml", "log":
		return "document"
	default:
		return "other"
	}
}

// HandleListFiles lists files safely within the requested subfolder
func HandleListFiles(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqRelPath := r.URL.Query().Get("path")
		if reqRelPath == "" {
			reqRelPath = "."
		}

		absTargetDir, err := SafePathJail(cfg.RootDir, reqRelPath)
		if err != nil {
			http.Error(w, `{"error":"Invalid directory path"}`, http.StatusBadRequest)
			return
		}

		info, err := os.Stat(absTargetDir)
		if err != nil {
			http.Error(w, `{"error":"Directory not found"}`, http.StatusNotFound)
			return
		}
		if !info.IsDir() {
			http.Error(w, `{"error":"Path is not a directory"}`, http.StatusBadRequest)
			return
		}

		entries, err := os.ReadDir(absTargetDir)
		if err != nil {
			http.Error(w, `{"error":"Failed to read directory"}`, http.StatusInternalServerError)
			return
		}

		var items []FileItem
		for _, entry := range entries {
			name := entry.Name()
			// Filter internal / hidden files
			if strings.HasPrefix(name, ".") || name == "nohup.out" || name == "__pycache__" || name == "secure_server.py" {
				continue
			}

			fullEntryPath := filepath.Join(absTargetDir, name)
			entryInfo, err := entry.Info()
			if err != nil {
				continue
			}

			relToRoot, err := filepath.Rel(cfg.RootDir, fullEntryPath)
			if err != nil {
				continue
			}

			ext := filepath.Ext(name)
			isDir := entry.IsDir()
			fileType := DetectFileType(ext, isDir)

			items = append(items, FileItem{
				Name:      name,
				Path:      filepath.ToSlash(relToRoot),
				IsDir:     isDir,
				Size:      entryInfo.Size(),
				HumanSize: FormatHumanSize(entryInfo.Size()),
				ModTime:   entryInfo.ModTime().Unix(),
				Type:      fileType,
				Ext:       strings.ToLower(ext),
			})
		}

		// Sort: directories first, then latest modified
		sort.Slice(items, func(i, j int) bool {
			if items[i].IsDir != items[j].IsDir {
				return items[i].IsDir // Dirs first
			}
			return items[i].ModTime > items[j].ModTime
		})

		currentRel, _ := filepath.Rel(cfg.RootDir, absTargetDir)
		if currentRel == "." {
			currentRel = ""
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"current_path": filepath.ToSlash(currentRel),
			"files":        items,
			"count":        len(items),
		})
	}
}

// GenerateUniqueName avoids overwriting existing files
func GenerateUniqueName(dir, baseFilename string) string {
	ext := filepath.Ext(baseFilename)
	nameWithoutExt := strings.TrimSuffix(baseFilename, ext)

	candidate := baseFilename
	counter := 1

	for {
		target := filepath.Join(dir, candidate)
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)%s", nameWithoutExt, counter, ext)
		counter++
	}
}

// HandleUpload handles atomic multipart file uploads
func HandleUpload(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		// Enforce maximum upload size limit
		maxBytes := cfg.MaxUploadMB * 1024 * 1024
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		// Parse multipart form
		err := r.ParseMultipartForm(32 << 20) // 32MB in RAM, rest to temp files
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"Upload exceeds maximum limit of %d MB"}`, cfg.MaxUploadMB), http.StatusRequestEntityTooLarge)
			return
		}

		targetFolder := r.FormValue("path")
		if targetFolder == "" {
			targetFolder = "media/uploads"
		}

		absTargetDir, err := SafePathJail(cfg.RootDir, targetFolder)
		if err != nil {
			http.Error(w, `{"error":"Invalid destination folder"}`, http.StatusBadRequest)
			return
		}

		if err := os.MkdirAll(absTargetDir, 0755); err != nil {
			http.Error(w, `{"error":"Could not create destination directory"}`, http.StatusInternalServerError)
			return
		}

		formFiles := r.MultipartForm.File["file"]
		if len(formFiles) == 0 {
			http.Error(w, `{"error":"No files attached"}`, http.StatusBadRequest)
			return
		}

		var uploadedFiles []string

		for _, fileHeader := range formFiles {
			// Sanitize filename strictly
			rawName := filepath.Base(fileHeader.Filename)
			// Strip directory separators and null bytes
			rawName = strings.ReplaceAll(rawName, "/", "_")
			rawName = strings.ReplaceAll(rawName, "\\", "_")
			rawName = strings.ReplaceAll(rawName, "\x00", "")
			if rawName == "" || rawName == "." || rawName == ".." {
				rawName = fmt.Sprintf("upload_%d", time.Now().UnixNano())
			}

			// Generate unique name to prevent accidental overwrite
			finalName := GenerateUniqueName(absTargetDir, rawName)
			finalPath := filepath.Join(absTargetDir, finalName)

			// Atomic write via temporary file
			tmpPath := filepath.Join(absTargetDir, fmt.Sprintf(".tmp_%d_%s", time.Now().UnixNano(), finalName))

			src, err := fileHeader.Open()
			if err != nil {
				http.Error(w, `{"error":"Failed to read uploaded file"}`, http.StatusInternalServerError)
				return
			}

			dst, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				src.Close()
				http.Error(w, `{"error":"Failed to initialize file on disk"}`, http.StatusInternalServerError)
				return
			}

			_, copyErr := io.Copy(dst, src)
			_ = dst.Sync()
			_ = dst.Close()
			_ = src.Close()

			if copyErr != nil {
				_ = os.Remove(tmpPath)
				http.Error(w, `{"error":"Failed during file transfer"}`, http.StatusInternalServerError)
				return
			}

			// Atomically rename into place
			if err := os.Rename(tmpPath, finalPath); err != nil {
				_ = os.Remove(tmpPath)
				http.Error(w, `{"error":"Failed to finalize file"}`, http.StatusInternalServerError)
				return
			}

			uploadedFiles = append(uploadedFiles, finalName)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "success",
			"uploaded": uploadedFiles,
			"count":    len(uploadedFiles),
		})
	}
}

// HandleDownload serves files with HTTP Range support and safe content disposition
func HandleDownload(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Query().Get("path")
		if reqPath == "" {
			http.Error(w, `{"error":"Path required"}`, http.StatusBadRequest)
			return
		}

		absPath, err := SafePathJail(cfg.RootDir, reqPath)
		if err != nil {
			http.Error(w, `{"error":"Access denied"}`, http.StatusForbidden)
			return
		}

		f, err := os.Open(absPath)
		if err != nil {
			http.Error(w, `{"error":"File not found"}`, http.StatusNotFound)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.Error(w, `{"error":"Cannot download directories directly"}`, http.StatusBadRequest)
			return
		}

		filename := filepath.Base(absPath)
		isDownload := r.URL.Query().Get("dl") == "1"

		ext := filepath.Ext(filename)
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Accept-Ranges", "bytes")

		if isDownload {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		} else {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
		}

		// http.ServeContent automatically handles RFC 7233 byte-range requests (HTTP 206 Partial Content)
		http.ServeContent(w, r, filename, info.ModTime(), f)
	}
}

// HandleDelete moves file to trash or permanently deletes if already in trash
func HandleDelete(cfg *Config) http.HandlerFunc {
	type DeleteRequest struct {
		Path      string `json:"path"`
		Permanent bool   `json:"permanent"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req DeleteRequest
		if r.Header.Get("Content-Type") == "application/json" {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"Invalid JSON payload"}`, http.StatusBadRequest)
				return
			}
		} else {
			req.Path = r.FormValue("path")
			req.Permanent = r.FormValue("permanent") == "true"
		}

		if req.Path == "" {
			http.Error(w, `{"error":"Path parameter missing"}`, http.StatusBadRequest)
			return
		}

		absPath, err := SafePathJail(cfg.RootDir, req.Path)
		if err != nil {
			http.Error(w, `{"error":"Access denied"}`, http.StatusForbidden)
			return
		}

		// Never allow deleting the root directory itself
		if absPath == cfg.RootDir {
			http.Error(w, `{"error":"Cannot delete root directory"}`, http.StatusForbidden)
			return
		}

		// Check if target is already inside trash or permanently requested
		relToTrash, err := filepath.Rel(cfg.TrashDir, absPath)
		isAlreadyInTrash := err == nil && !strings.HasPrefix(relToTrash, "..")

		if req.Permanent || isAlreadyInTrash {
			if err := os.RemoveAll(absPath); err != nil {
				http.Error(w, `{"error":"Failed to remove item"}`, http.StatusInternalServerError)
				return
			}
		} else {
			// Move to trash
			baseName := filepath.Base(absPath)
			trashName := fmt.Sprintf("%d_%s", time.Now().Unix(), baseName)
			trashDest := filepath.Join(cfg.TrashDir, trashName)

			if err := os.Rename(absPath, trashDest); err != nil {
				http.Error(w, `{"error":"Failed to move item to trash"}`, http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"message": "Item deleted successfully",
		})
	}
}

// HandleEmptyTrash permanently deletes every entry inside the trash directory.
// It only ever touches cfg.TrashDir; the rest of the storage root is untouched.
func HandleEmptyTrash(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		entries, err := os.ReadDir(cfg.TrashDir)
		if err != nil {
			if os.IsNotExist(err) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "deleted": 0})
				return
			}
			http.Error(w, `{"error":"Failed to read trash"}`, http.StatusInternalServerError)
			return
		}

		deleted := 0
		for _, entry := range entries {
			// Re-check containment so a crafted entry name can never escape trash.
			if _, err := SafePathJail(cfg.TrashDir, entry.Name()); err != nil {
				continue
			}
			if err := os.RemoveAll(filepath.Join(cfg.TrashDir, entry.Name())); err != nil {
				http.Error(w, `{"error":"Failed to empty trash"}`, http.StatusInternalServerError)
				return
			}
			deleted++
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":  "success",
			"deleted": deleted,
			"message": "Trash emptied",
		})
	}
}

// HandleRename safely renames a file or folder
func HandleRename(cfg *Config) http.HandlerFunc {
	type RenameRequest struct {
		OldPath string `json:"old_path"`
		NewName string `json:"new_name"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodPatch {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req RenameRequest
		if r.Header.Get("Content-Type") == "application/json" {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"Invalid JSON"}`, http.StatusBadRequest)
				return
			}
		} else {
			req.OldPath = r.FormValue("old_path")
			req.NewName = r.FormValue("new_name")
		}

		if req.OldPath == "" || req.NewName == "" {
			http.Error(w, `{"error":"Missing old_path or new_name"}`, http.StatusBadRequest)
			return
		}

		// Ensure new_name has NO directory components
		cleanNewName := filepath.Base(req.NewName)
		if cleanNewName == "." || cleanNewName == ".." || cleanNewName == "" || strings.ContainsAny(req.NewName, "/\\") {
			http.Error(w, `{"error":"Invalid new name"}`, http.StatusBadRequest)
			return
		}

		absOldPath, err := SafePathJail(cfg.RootDir, req.OldPath)
		if err != nil {
			http.Error(w, `{"error":"Access denied on source path"}`, http.StatusForbidden)
			return
		}

		if absOldPath == cfg.RootDir {
			http.Error(w, `{"error":"Cannot rename root directory"}`, http.StatusForbidden)
			return
		}

		parentDir := filepath.Dir(absOldPath)
		absNewPath := filepath.Join(parentDir, cleanNewName)

		// Validate new target path within jail
		if _, err := SafePathJail(cfg.RootDir, absNewPath); err != nil {
			http.Error(w, `{"error":"Invalid target path"}`, http.StatusForbidden)
			return
		}

		// Check if new target already exists
		if _, err := os.Stat(absNewPath); err == nil {
			http.Error(w, `{"error":"A file or directory with this name already exists"}`, http.StatusConflict)
			return
		}

		if err := os.Rename(absOldPath, absNewPath); err != nil {
			http.Error(w, `{"error":"Failed to rename"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "success",
			"new_name": cleanNewName,
		})
	}
}

// HandleMkdir safely creates a new directory inside jail
func HandleMkdir(cfg *Config) http.HandlerFunc {
	type MkdirRequest struct {
		ParentPath string `json:"parent_path"`
		DirName    string `json:"dir_name"`
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req MkdirRequest
		if r.Header.Get("Content-Type") == "application/json" {
			_ = json.NewDecoder(r.Body).Decode(&req)
		} else {
			req.ParentPath = r.FormValue("parent_path")
			req.DirName = r.FormValue("dir_name")
		}

		cleanName := filepath.Base(req.DirName)
		if cleanName == "" || cleanName == "." || cleanName == ".." || strings.ContainsAny(req.DirName, "/\\") {
			http.Error(w, `{"error":"Invalid directory name"}`, http.StatusBadRequest)
			return
		}

		absParent, err := SafePathJail(cfg.RootDir, req.ParentPath)
		if err != nil {
			http.Error(w, `{"error":"Invalid parent path"}`, http.StatusBadRequest)
			return
		}

		target := filepath.Join(absParent, cleanName)
		if _, err := SafePathJail(cfg.RootDir, target); err != nil {
			http.Error(w, `{"error":"Access denied"}`, http.StatusForbidden)
			return
		}

		if err := os.Mkdir(target, 0755); err != nil {
			if errors.Is(err, os.ErrExist) {
				http.Error(w, `{"error":"Directory already exists"}`, http.StatusConflict)
				return
			}
			http.Error(w, `{"error":"Failed to create directory"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "created": cleanName})
	}
}
