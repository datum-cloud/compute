package exec

import (
	"context"
	"time"

	"golang.org/x/term"

	"go.datum.net/compute/internal/consoleclient"
)

func forwardResizes(ctx context.Context, fd int, s *consoleclient.Stream) {
	var lastW, lastH int
	for {
		if w, h, err := term.GetSize(fd); err == nil && (w != lastW || h != lastH) {
			lastW, lastH = w, h
			_ = s.Resize(uint16(w), uint16(h))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
