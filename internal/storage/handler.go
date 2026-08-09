package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"

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

	reader, contentType, err := h.svc.DownloadFile(r.Context(), userID, fileID)
	if err != nil {
		if errors.Is(err, ErrAccessDenied) || errors.Is(err, ErrFileNotFound) {
			http.Error(w, "file not found or access denied", http.StatusForbidden)
			return
		}
		tracing.Logger(r.Context()).Error("download file service error", "error", err, "file_id", fileID, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

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

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)

	_, err = io.Copy(w, buffered)
	if err != nil {
		tracing.Logger(r.Context()).Error("error streaming file to client", "error", err, "file_id", fileID, "user_id", userID)
	} else {
		tracing.Logger(r.Context()).Info("file downloaded successfully", "file_id", fileID, "user_id", userID)
	}
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
