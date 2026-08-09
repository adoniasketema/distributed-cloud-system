package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"nimbus/internal/tracing"
)

// Handler handles HTTP requests for auth endpoints
type Handler struct {
	svc Service
}

// NewHandler creates a new auth HTTP handler
func NewHandler(svc Service) *Handler {
	return &Handler{svc: svc}
}

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
}

// decodeRequest reads the JSON body, distinguishing a body that exceeded the configured
// limit (413) from one that is merely malformed (400).
func (h *Handler) decodeRequest(w http.ResponseWriter, r *http.Request, dst *authRequest) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.respondError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		h.respondError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// Register handles user registration
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !h.decodeRequest(w, r, &req) {
		return
	}

	if req.Email == "" || req.Password == "" {
		h.respondError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	_, err := h.svc.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		tracing.Logger(r.Context()).Error("user registration failed", "error", err, "email", req.Email)
		h.respondError(w, http.StatusInternalServerError, "failed to register user")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"message": "user registered successfully"})
}

// Login handles user login and JWT issuance
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !h.decodeRequest(w, r, &req) {
		return
	}

	token, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		tracing.Logger(r.Context()).Warn("login attempt failed", "error", err, "email", req.Email)
		h.respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(authResponse{Token: token})
}

// Helper for sending error responses
func (h *Handler) respondError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(authResponse{Error: message})
}
