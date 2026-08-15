package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

type healthzResponse struct {
	Status string `json:"status"`
}

func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(healthzResponse{Status: "ok"}); err != nil {
		slog.ErrorContext(r.Context(), "encode healthz response", "err", err)
	}
}
