package metadata

import (
	"encoding/json"
	"errors"
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

type createFolderRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
}

func (h *Handler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req createFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "folder name is required", http.StatusBadRequest)
		return
	}

	folder, err := h.svc.CreateFolder(r.Context(), userID, req.ParentID, req.Name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "parent folder not found or access denied", http.StatusForbidden)
			return
		}
		if errors.Is(err, ErrDuplicateName) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "folder with this name already exists in directory"})
			return
		}
		tracing.Logger(r.Context()).Error("create folder failed", "error", err, "user_id", userID, "folder_name", req.Name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(folder)
}

func (h *Handler) ListDirectory(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var folderIDPtr *string
	folderID := r.URL.Query().Get("folder_id")
	if folderID != "" {
		folderIDPtr = &folderID
	}

	content, err := h.svc.ListDirectory(r.Context(), userID, folderIDPtr)
	if err != nil {
		tracing.Logger(r.Context()).Error("list directory failed", "error", err, "user_id", userID, "folder_id", folderID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(content)
}

func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	folderID := r.PathValue("id")
	if folderID == "" {
		http.Error(w, "Invalid folder ID", http.StatusBadRequest)
		return
	}

	err := h.svc.DeleteFolder(r.Context(), userID, folderID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "folder not found or access denied", http.StatusForbidden)
			return
		}
		tracing.Logger(r.Context()).Error("delete folder failed", "error", err, "folder_id", folderID, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SearchFiles(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(string)
	if !ok || userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	query := r.URL.Query().Get("q")

	files, err := h.svc.SearchFiles(r.Context(), userID, query)
	if err != nil {
		tracing.Logger(r.Context()).Error("search files failed", "error", err, "query", query, "user_id", userID)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(files)
}
