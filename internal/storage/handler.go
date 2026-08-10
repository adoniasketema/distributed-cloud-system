package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"nimbus/internal/middleware"
	"nimbus/internal/tracing"
)

type Handler struct {
	svc Service
}

func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Enforce hard maximum limit on request body size (100 MB)
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	err := r.ParseMultipartForm(100 << 20)
	if err != nil {
		http.Error(w, "Error parsing form data or file too large (max 100MB)", http.StatusBadRequest)
		return
	}

	folderIDStr := r.FormValue("folder_id")
	var folderID *string
	if folderIDStr != "" {
		folderID = &folderIDStr
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File not present in request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	fileID, err := h.svc.UploadFile(r.Context(), userID, folderID, header.Filename, file)
	if err != nil {
		if errors.Is(err, ErrAccessDenied) {
			http.Error(w, "folder not found or access denied", http.StatusForbidden)
			return
		}
		if errors.Is(err, ErrDuplicateName) {
			// An expected outcome of a valid request, not a server fault.
			http.Error(w, "a file with this name already exists in this directory", http.StatusConflict)
			return
		}
		tracing.Logger(r.Context()).Error("upload file service failed", "error", err, "filename", header.Filename, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	tracing.Logger(r.Context()).Info("file uploaded successfully", "file_id", fileID, "filename", header.Filename, "user_id", userID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"message": "file uploaded successfully",
		"file_id": fileID,
	})
}

func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	fileID := r.PathValue("id")
	if fileID == "" {
		http.Error(w, "Invalid file ID", http.StatusBadRequest)
		return
	}

	reader, info, err := h.svc.DownloadFile(r.Context(), userID, fileID)
	if err != nil {
		if errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrFileNotFound) {
			http.Error(w, "file not found or access denied", http.StatusForbidden)
			return
		}
		tracing.Logger(r.Context()).Error("download file service error", "error", err, "file_id", fileID, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	contentType := info.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Pull the first byte before committing a status code. Chunks are integrity-checked when
	// they are first read, so this turns a corrupt or missing first chunk into an honest 500
	// instead of a 200 followed by a truncated body. Peeked bytes are replayed by io.Copy.
	buffered := bufio.NewReader(reader)
	if _, err := buffered.Peek(1); err != nil && err != io.EOF {
		tracing.Logger(r.Context()).Error("download failed before streaming", "error", err, "file_id", fileID, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Content-Type here is whatever http.DetectContentType made of user-supplied bytes, so an
	// uploaded HTML or SVG file would otherwise render in the browser on this API's own
	// origin - stored XSS against any session cookie scoped to it. Forcing an attachment
	// disposition, and telling the browser not to sniff past the declared type, makes the
	// response a download in every case.
	w.Header().Set("Content-Disposition", contentDisposition(info.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)

	_, err = io.Copy(w, buffered)
	if err != nil {
		tracing.Logger(r.Context()).Error("error streaming file to client", "error", err, "file_id", fileID, "user_id", userID)
	} else {
		tracing.Logger(r.Context()).Info("file downloaded successfully", "file_id", fileID, "user_id", userID)
	}
}

// contentDisposition builds an attachment disposition for a user-supplied filename.
//
// The name arrives in the multipart upload header, so it is attacker-controlled and may hold
// quotes, control characters, path separators, or non-ASCII bytes. It is reduced to a bare
// base name and stripped of control characters, then handed to mime.FormatMediaType, which
// applies quoting and RFC 2231 encoding. FormatMediaType returns an empty string for input it
// cannot represent, so a fixed fallback covers that case rather than emitting a bare header.
func contentDisposition(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)

	switch name {
	case "", ".", "..", "/":
		name = "download"
	}

	if formatted := mime.FormatMediaType("attachment", map[string]string{"filename": name}); formatted != "" {
		return formatted
	}
	return `attachment; filename="download"`
}

func (h *Handler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	fileID := r.PathValue("id")
	if fileID == "" {
		http.Error(w, "Invalid file ID", http.StatusBadRequest)
		return
	}

	err := h.svc.DeleteFile(r.Context(), userID, fileID)
	if err != nil {
		if errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrFileNotFound) {
			http.Error(w, "file not found or access denied", http.StatusForbidden)
			return
		}
		tracing.Logger(r.Context()).Error("delete file service error", "error", err, "file_id", fileID, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	tracing.Logger(r.Context()).Info("file deleted successfully", "file_id", fileID, "user_id", userID)
	w.WriteHeader(http.StatusNoContent)
}
