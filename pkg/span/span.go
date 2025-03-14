package span

import (
	"context"
	"fmt"
	"udisend/pkg/ctxtool"
)

func Init(span string, args ...any) context.Context {
	span = fmt.Sprintf(span, args...)
	return context.WithValue(context.Background(), ctxtool.KeySpan, span)
}

func Extend(ctx context.Context, span string, args ...any) context.Context {
	span = fmt.Sprintf(span, args...)
	v := ctx.Value(ctxtool.KeySpan)
	if v == nil {
		return context.WithValue(ctx, ctxtool.KeySpan, span)
	}

	actualSpan := v.(string)
	return context.WithValue(ctx, ctxtool.KeySpan, fmt.Sprintf("%s: %s", actualSpan, span))
}
