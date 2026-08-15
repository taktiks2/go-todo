package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/taktiks2/go-todo/backend/internal/config"
	httpapi "github.com/taktiks2/go-todo/backend/internal/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	h := httpapi.NewHandler()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: h.Routes(),
	}

	slog.Info("starting server", "addr", srv.Addr)

	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
