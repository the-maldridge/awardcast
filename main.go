package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/the-maldridge/awardcast/pkg/server"
)

func main() {
	slog.SetDefault(logger())

	if err := run(); err != nil {
		slog.Error("Fatal error", "error", err)
		os.Exit(1)
	}
}

// run builds the application and blocks in it until the process is asked to
// stop. The framework owns the signal handling and the graceful drain, so a
// shutdown that runs out of time is reported by the framework rather than by a
// second implementation here.
func run() error {
	app, err := server.New()
	if err != nil {
		return err
	}
	defer app.Close()

	return app.Run()
}

// logger builds the process logger from LOG_LEVEL. The application falls back
// to this logger when nothing is passed to webapp.WithLogger.
func logger() *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(os.Getenv("LOG_LEVEL")))); err != nil {
		level = slog.LevelInfo
	}

	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
