package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"payment-mock-autoshop/internal/mock"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	port := env("PORT", "8080")
	cfg := mock.Config{
		ClientID:     env("MOCK_CLIENT_ID", "autoshop-client"),
		ClientSecret: env("MOCK_CLIENT_SECRET", "autoshop-secret"),
		PublicURL:    env("MOCK_PUBLIC_URL", "http://localhost:"+port),
		PaymentDelay: duration("MOCK_PAYMENT_DELAY", 5*time.Second),
		TokenTTL:     duration("MOCK_TOKEN_TTL", time.Hour),
	}
	server := mock.NewServer(cfg, mock.NewStore(), mock.NewHTTPNotifier(log), log)

	srv := &http.Server{Addr: ":" + port, Handler: server.Routes(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("payment-mock ouvindo", "port", port, "payment_delay", cfg.PaymentDelay.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("erro no servidor", "error", err.Error())
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func duration(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return fallback
}
