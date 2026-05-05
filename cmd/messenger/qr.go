package main

import (
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// renderQR writes an ANSI half-block rendering of `data` to `w`. Two QR
// rows are packed per terminal line via U+2580 (▀) — black-on-top vs.
// black-on-bottom — to keep the QR roughly square in normal terminal
// fonts. We use unicode-block instead of monolithic ASCII (`██` per cell)
// so a default-sized QR fits in 40-50 columns instead of 80-100.
func renderQR(w io.Writer, data string) error {
	code, err := qr.Encode(data, qr.M)
	if err != nil {
		return fmt.Errorf("qr encode: %w", err)
	}
	size := code.Size

	// 2-pixel padding (one block row = two QR rows) all around so terminal
	// scanners can detect the quiet zone.
	const pad = 2
	top := strings.Repeat(" ", (size+2*pad)*1)

	black := func(x, y int) bool {
		if x < pad || y < pad || x >= size+pad || y >= size+pad {
			return false
		}
		return code.Black(x-pad, y-pad)
	}

	// Top quiet line.
	for range pad / 2 {
		fmt.Fprintln(w, top)
	}

	for y := 0; y < size+2*pad; y += 2 {
		var line strings.Builder
		for x := 0; x < size+2*pad; x++ {
			tBlack := black(x, y)
			bBlack := black(x, y+1)
			switch {
			case tBlack && bBlack:
				line.WriteString("\033[30;47m█\033[0m")
			case tBlack && !bBlack:
				line.WriteString("\033[30;47m▀\033[0m")
			case !tBlack && bBlack:
				line.WriteString("\033[30;47m▄\033[0m")
			default:
				line.WriteString("\033[30;47m \033[0m")
			}
		}
		fmt.Fprintln(w, line.String())
	}

	return nil
}
