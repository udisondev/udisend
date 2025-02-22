package ctxtool

import (
	"context"
	"fmt"
)

func Span(ctx context.Context, span string) context.Context {
	v := ctx.Value(KeySpan)
	if v == nil {
		return context.WithValue(ctx, KeySpan, span)
	}

	actualSpan := v.(string)
	return context.WithValue(ctx, KeySpan, fmt.Sprintf("%s: %s", actualSpan, span))
}
