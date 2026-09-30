//go:build !windows

package exec

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"go.datum.net/compute/internal/consoleclient"
)

func forwardResizes(ctx context.Context, fd int, s *consoleclient.Stream) {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	for {
		if w, h, err := term.GetSize(fd); err == nil {
			_ = s.Resize(uint16(w), uint16(h))
		}
		select {
		case <-ctx.Done():
			return
		case <-winch:
		}
	}
}
