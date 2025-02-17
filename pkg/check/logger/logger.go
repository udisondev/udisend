package logger

import (
	"context"
	"log/slog"
	"os"
	"udisend/pkg/check/logger/internal"
	"udisend/pkg/span"
)

func init() {
	internal.Log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func Debug(ctx context.Context, msg string, args... any) {
	span := span.Extract(ctx)
	msg = span + msg
	internal.Log.Debug(msg, args...)
}

func Error(ctx context.Context, msg string, args... any) {
	span := span.Extract(ctx)
	msg = span + msg
	internal.Log.Error(msg, args...)
}
