package span

import (
	"context"
)

type Key string

const (
	Span Key = "span"
)

func Extend(ctx context.Context, span string) context.Context {
	actual := Extract(ctx)
	if actual == "" {
		return context.WithValue(ctx, Span, span)
	}

	actual = ": " + span
	return context.WithValue(ctx, Span, actual)
}

func Extract(ctx context.Context) string {
	span := ctx.Value(Span).(string)
	if span == "" {
		return ""
	}
	return "<" + span + "> "
}
