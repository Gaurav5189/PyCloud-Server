package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
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

// maxUploadNameBytes keeps the final filename plus the temporary-file prefix
// below the common 255-byte per-component filesystem limit.
const maxUploadNameBytes = 200

// sanitizeUploadFilename strips path components, null bytes and separators, and
// truncates over-long names on a UTF-8 boundary while preserving the extension.
func sanitizeUploadFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.ToValidUTF8(name, "_")

	if name == "" || name == "." || name == ".." {
		return fmt.Sprintf("upload_%d", time.Now().UnixNano())
	}

	if len(name) <= maxUploadNameBytes {
		return name
	}

	ext := filepath.Ext(name)
	if len(ext) > 20 {
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	limit := maxUploadNameBytes - len(ext)
	if limit < 1 {
		limit = 1
	}
	if len(base) > limit {
		// Never cut a multi-byte rune in half; the filesystem would reject it.
		for limit > 0 && !utf8.RuneStart(base[limit]) {
			limit--
		}
		base = base[:limit]
	}
	return base + ext
}

// writeUploadError sends a JSON error body (the SPA parses it) with the status
// code that actually describes what went wrong.
func writeUploadError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// uploadErrorStatus maps a multipart read/write failure to a useful status and
// message. Treating every ParseMultipartForm error as "exceeds maximum limit"
// hid network drops, stalls and full disks behind a misleading 413.
func uploadErrorStatus(maxMB int64, err error, phase string) (int, string) {
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		return http.StatusRequestEntityTooLarge,
			fmt.Sprintf("Upload exceeds maximum limit of %d MB", maxMB)

	case errors.Is(err, syscall.ENOSPC):
		return http.StatusInsufficientStorage,
			"The server ran out of disk space while " + phase + " the upload."

	case errors.Is(err, os.ErrPermission):
		return http.StatusInternalServerError,
			"The server could not " + phase + " the upload: permission denied."

	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout,
			"Upload timed out while " + phase + " the upload because the connection stalled. Try again on a stable network."

	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed),
		errors.Is(err, syscall.ECONNRESET), errors.Is(err, context.Canceled):
		return http.StatusBadRequest,
			"Upload was interrupted while " + phase + " the upload. Check your connection and try again."

	default:
		return http.StatusBadRequest,
			"The upload could not be read while " + phase + " it: the request was malformed or interrupted."
	}
}

// uploadTempPrefix marks a partial upload this package writes beside its
// destination; such files are always renamed away or removed before the request
// returns, so any that survive a crash are safe to delete at startup.
const uploadTempPrefix = ".tmp_"

// isUploadTempName matches this package's ".tmp_<unixnano>_<name>" files. The
// numeric component matters: a user file that merely starts with ".tmp_" must
// never be mistaken for a leftover and deleted.
func isUploadTempName(name string) bool {
	if !strings.HasPrefix(name, uploadTempPrefix) {
		return false
	}
	rest := name[len(uploadTempPrefix):]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	return i > 0 && i < len(rest) && rest[i] == '_'
}

// cleanupStaleUploadTemps removes partial upload files left behind by a crash or
// a hard kill. It only deletes files older than cutoff, so it can run in the
// background while new uploads are in flight without touching their temp files.
func cleanupStaleUploadTemps(root string, cutoff time.Time) {
	removed := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !isUploadTempName(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
		return nil
	})
	if removed > 0 {
		log.Printf("🧹 Removed %d stale upload temporary file(s)", removed)
	}
}

// commitUpload moves a finished temporary file to a unique final name. os.Link
// fails when the target already exists, which closes the window between naming
// and moving that two concurrent uploads of the same filename could otherwise
// race through; the rename is only a fallback for filesystems without hard
// links.
func commitUpload(tmpPath, dir, rawName string) (string, error) {
	for attempt := 0; attempt < 50; attempt++ {
		finalName := GenerateUniqueName(dir, rawName)
		finalPath := filepath.Join(dir, finalName)

		if err := os.Link(tmpPath, finalPath); err == nil {
			_ = os.Remove(tmpPath)
			return finalName, nil
		} else if errors.Is(err, fs.ErrExist) {
			continue
		}

		if err := os.Rename(tmpPath, finalPath); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", fmt.Errorf("finalize upload: %w", err)
		}
		return finalName, nil
	}
	return "", fmt.Errorf("finalize upload: could not find a unique name for %q", rawName)
}

// saveUploadStream writes one file to a temporary file in the target directory
// and commits it only after the copy is complete. The temporary file lives
// beside the destination, so an upload never needs a second copy's worth of
// space in the system temp directory.
func saveUploadStream(src io.Reader, filename, dir string) (string, error) {
	rawName := sanitizeUploadFilename(filename)
	tmpPath := filepath.Join(dir, fmt.Sprintf("%s%d_%s", uploadTempPrefix, time.Now().UnixNano(), rawName))

	dst, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	// No-op after a successful commit; guarantees no partial file is left behind
	// on any error or panic.
	defer os.Remove(tmpPath)

	_, copyErr := io.Copy(dst, src)
	syncErr := dst.Sync()
	closeErr := dst.Close()

	if copyErr != nil {
		return "", fmt.Errorf("write upload: %w", copyErr)
	}
	if syncErr != nil {
		return "", fmt.Errorf("sync upload: %w", syncErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close upload: %w", closeErr)
	}

	return commitUpload(tmpPath, dir, rawName)
}

// saveUploadedFile writes one parsed multipart file to the target directory.
func saveUploadedFile(fileHeader *multipart.FileHeader, dir string) (string, error) {
	src, err := fileHeader.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	defer src.Close()
	return saveUploadStream(src, fileHeader.Filename, dir)
}

// uploadResult is the JSON body returned after a successful upload.
func uploadResult(w http.ResponseWriter, uploaded []string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "success",
		"uploaded": uploaded,
		"count":    len(uploaded),
	})
}

// rollbackUploads removes files already committed by this request so a retry
// cannot leave half an upload behind.
func rollbackUploads(dir string, names []string) {
	for _, name := range names {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			log.Printf("⚠️  upload rollback could not remove %q: %v", name, err)
		}
	}
}

var (
	errUploadDirInvalid = errors.New("invalid destination folder")
	errUploadDirTrash   = errors.New("trash is read-only for uploads")
)

// isInside reports whether candidate is dir itself or a descendant of it.
func isInside(dir, candidate string) bool {
	rel, err := filepath.Rel(dir, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// prepareUploadDir validates and creates the destination. Trash is read-only for
// uploads: the SPA already refuses it, and this makes the API refuse it too.
func prepareUploadDir(cfg *Config, targetFolder string) (string, error) {
	absTargetDir, err := SafePathJail(cfg.RootDir, targetFolder)
	if err != nil {
		return "", errUploadDirInvalid
	}
	// Compare against the jail-resolved trash path so a symlinked root_dir
	// cannot make the check miss.
	if realTrash, err := SafePathJail(cfg.RootDir, ".pycloud_trash"); err == nil && isInside(realTrash, absTargetDir) {
		return "", errUploadDirTrash
	}
	if err := os.MkdirAll(absTargetDir, 0755); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}
	return absTargetDir, nil
}

// writeUploadDirError turns a prepareUploadDir failure into the matching status
// and message.
func writeUploadDirError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUploadDirInvalid):
		writeUploadError(w, http.StatusBadRequest, "Invalid destination folder")
	case errors.Is(err, errUploadDirTrash):
		writeUploadError(w, http.StatusBadRequest, "Trash is read-only for uploads. Choose another folder.")
	default:
		writeUploadError(w, http.StatusInternalServerError, "Could not create destination directory")
	}
}

// handleStreamingUpload reads a multipart body part-by-part and writes each file
// directly to its destination. This is used when the client supplies the target
// folder in the query string (the SPA always does), so a 4GB upload does not
// first fill /tmp with a second copy.
func handleStreamingUpload(w http.ResponseWriter, r *http.Request, cfg *Config, targetFolder string, maxMB int64) {
	absTargetDir, err := prepareUploadDir(cfg, targetFolder)
	if err != nil {
		writeUploadDirError(w, err)
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeUploadError(w, http.StatusBadRequest,
			"The upload could not be read: the request was not valid multipart form data.")
		return
	}

	uploadedFiles := make([]string, 0, 1)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			rollbackUploads(absTargetDir, uploadedFiles)
			status, message := uploadErrorStatus(maxMB, err, "receiving")
			writeUploadError(w, status, message)
			return
		}

		// Only the "file" field carries uploads. The destination came from the
		// query string, so the "path" form field and any other metadata are
		// skipped.
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}

		finalName, err := saveUploadStream(part, part.FileName(), absTargetDir)
		_ = part.Close()
		if err != nil {
			rollbackUploads(absTargetDir, uploadedFiles)
			status, message := uploadErrorStatus(maxMB, err, "saving")
			writeUploadError(w, status, message)
			return
		}
		uploadedFiles = append(uploadedFiles, finalName)
	}

	if len(uploadedFiles) == 0 {
		writeUploadError(w, http.StatusBadRequest, "No files attached")
		return
	}
	uploadResult(w, uploadedFiles)
}

// HandleUpload handles multipart file uploads. All files in one request are
// committed together: if a later file fails, earlier ones are removed so a
// retry cannot leave half an upload behind.
func HandleUpload(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"Method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		maxMB := cfg.MaxUploadMB
		if maxMB <= 0 {
			maxMB = DefaultMaxUploadMB
		}
		maxBytes := maxMB * 1024 * 1024

		// Reject an oversized request before reading a single byte of it. This
		// only catches clients that send Content-Length; MaxBytesReader below is
		// the real enforcement for chunked bodies.
		if r.ContentLength > maxBytes {
			writeUploadError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Upload exceeds maximum limit of %d MB", maxMB))
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		// Prefer the destination from the query string: knowing it up front lets
		// the body stream straight to the target directory. Clients that only
		// send the multipart "path" field still work through the buffered path.
		// If middleware already parsed the body (a CSRF token sent as a form
		// field rather than a header), streaming is no longer possible.
		if targetFolder := r.URL.Query().Get("path"); targetFolder != "" && r.MultipartForm == nil {
			handleStreamingUpload(w, r, cfg, targetFolder, maxMB)
			return
		}

		// Parse multipart form (32MB in RAM, the rest to temporary files).
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			status, message := uploadErrorStatus(maxMB, err, "receiving")
			writeUploadError(w, status, message)
			return
		}
		defer r.MultipartForm.RemoveAll()

		targetFolder := r.FormValue("path")
		if targetFolder == "" {
			targetFolder = "media/uploads"
		}

		absTargetDir, err := prepareUploadDir(cfg, targetFolder)
		if err != nil {
			writeUploadDirError(w, err)
			return
		}

		formFiles := r.MultipartForm.File["file"]
		if len(formFiles) == 0 {
			writeUploadError(w, http.StatusBadRequest, "No files attached")
			return
		}

		uploadedFiles := make([]string, 0, len(formFiles))
		for _, fileHeader := range formFiles {
			finalName, err := saveUploadedFile(fileHeader, absTargetDir)
			if err != nil {
				rollbackUploads(absTargetDir, uploadedFiles)
				status, message := uploadErrorStatus(maxMB, err, "saving")
				writeUploadError(w, status, message)
				return
			}
			uploadedFiles = append(uploadedFiles, finalName)
		}

		uploadResult(w, uploadedFiles)
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
