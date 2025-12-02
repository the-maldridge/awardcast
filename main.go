package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/the-maldridge/authware/backend/htpasswd"
	_ "github.com/the-maldridge/authware/backend/netauth"

	"github.com/the-maldridge/awardcast/pkg/server"
)

func main() {
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	s, err := server.New()
	if err != nil {
		slog.Error("Error initializing server", "error", err)
		return
	}
	serverCtx, serverStopCtx := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sig

		shutdownCtx, _ := context.WithTimeout(serverCtx, 30*time.Second)

		go func() {
			<-shutdownCtx.Done()
			if shutdownCtx.Err() == context.DeadlineExceeded {
				slog.Error("Graceful shutdown timed out.. forcing exit.")
				os.Exit(5)
			}
		}()

		err := s.Shutdown(shutdownCtx)
		if err != nil {
			slog.Error("Error occured during shutdown", "error", err)
		}
		serverStopCtx()

	}()

	bind := os.Getenv("AWARD_ADDR")
	if bind == "" {
		bind = ":1323"
	}
	s.Serve(bind)
	<-serverCtx.Done()
}
