package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Allowed streaming extensions to prevent arbitrary file reading via media endpoint
var allowedMediaExts = map[string]string{
	".mp4":  "video/mp4",
	".mkv":  "video/x-matroska",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".avi":  "video/x-msvideo",
	".m4v":  "video/mp4",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".opus": "audio/opus",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",
}

// HandleMediaStream streams video and audio with byte-range resume support
func HandleMediaStream(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Query().Get("path")
		if reqPath == "" {
			http.Error(w, `{"error":"Missing path"}`, http.StatusBadRequest)
			return
		}

		absPath, err := SafePathJail(cfg.RootDir, reqPath)
		if err != nil {
			http.Error(w, `{"error":"Access denied"}`, http.StatusForbidden)
			return
		}

		ext := strings.ToLower(filepath.Ext(absPath))
		contentType, allowed := allowedMediaExts[ext]
		if !allowed {
			http.Error(w, `{"error":"File type not supported for media streaming"}`, http.StatusUnsupportedMediaType)
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
			http.Error(w, `{"error":"Invalid media file"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Accept-Ranges", "bytes")

		// Serve with full Range support
		http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), f)
	}
}

// HandleSubtitles serves subtitle files (.vtt / .srt)
func HandleSubtitles(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqPath := r.URL.Query().Get("path")
		if reqPath == "" {
			http.Error(w, "Path required", http.StatusBadRequest)
			return
		}

		absVideoPath, err := SafePathJail(cfg.RootDir, reqPath)
		if err != nil {
			http.Error(w, "Access denied", http.StatusForbidden)
			return
		}

		baseNoExt := strings.TrimSuffix(absVideoPath, filepath.Ext(absVideoPath))
		candidates := []string{
			baseNoExt + ".vtt",
			baseNoExt + ".en.vtt",
			baseNoExt + ".srt",
		}

		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				f, err := os.Open(candidate)
				if err != nil {
					continue
				}
				defer f.Close()

				w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				http.ServeContent(w, r, filepath.Base(candidate), info.ModTime(), f)
				return
			}
		}

		http.Error(w, "No subtitle track found", http.StatusNotFound)
	}
}

// HandleMediaInfo returns player details and subtitle presence
func HandleMediaInfo(cfg *Config) http.HandlerFunc {
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

		info, err := os.Stat(absPath)
		if err != nil {
			http.Error(w, `{"error":"Media not found"}`, http.StatusNotFound)
			return
		}

		ext := strings.ToLower(filepath.Ext(absPath))
		baseNoExt := strings.TrimSuffix(absPath, ext)
		hasSubtitles := false
		if _, err := os.Stat(baseNoExt + ".vtt"); err == nil {
			hasSubtitles = true
		} else if _, err := os.Stat(baseNoExt + ".srt"); err == nil {
			hasSubtitles = true
		}

		mimeType := allowedMediaExts[ext]
		if mimeType == "" {
			mimeType = mime.TypeByExtension(ext)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"filename":      filepath.Base(absPath),
			"path":          filepath.ToSlash(reqPath),
			"size":          info.Size(),
			"human_size":    FormatHumanSize(info.Size()),
			"mime_type":     mimeType,
			"has_subtitles": hasSubtitles,
			"subtitle_url":  fmt.Sprintf("/api/media/subtitles?path=%s", reqPath),
			"stream_url":    fmt.Sprintf("/api/media/stream?path=%s", reqPath),
		})
	}
}
