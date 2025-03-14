package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"udisend/pkg/ctxtool"
)

var logger *slog.Logger

func init() {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func Debugf(ctx context.Context, msg string, args ...any) {
	msg = fmt.Sprintf(msg, args...)
	if ctx != nil {
		msg = upgradeMsg(ctx, msg)
	}
	logger.Debug(msg)
}

func Infof(ctx context.Context, msg string, args ...any) {
	msg = fmt.Sprintf(msg, args...)
	if ctx != nil {
		msg = upgradeMsg(ctx, msg)
	}
	logger.Info(msg)
}

func Warnf(ctx context.Context, msg string, args ...any) {
	msg = fmt.Sprintf(msg, args...)
	if ctx != nil {
		msg = upgradeMsg(ctx, msg)
	}
	logger.Warn(msg)
}

func Errorf(ctx context.Context, msg string, args ...any) {
	msg = fmt.Sprintf(msg, args...)
	if ctx != nil {
		msg = upgradeMsg(ctx, msg)
	}
	logger.Error(msg)
}

func upgradeMsg(ctx context.Context, msg string) string {
	v := ctx.Value(ctxtool.KeySpan)
	if v == nil {
		return msg
	}
	span := v.(string)
	msg = fmt.Sprintf("<%s> %s", span, msg)

	return msg
}
