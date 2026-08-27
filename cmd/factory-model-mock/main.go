package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/modelmock"
)

func main() {
	server := &http.Server{Addr: ":8091", Handler: new(modelmock.Server).Routes(), ReadHeaderTimeout: 5 * time.Second}
	slog.Info("local demo model provider listening", "address", server.Addr, "production_evidence", false)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("local demo model provider stopped", "error", err)
		os.Exit(1)
	}
}
