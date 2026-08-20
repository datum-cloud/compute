package build

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

type spinner struct {
	out         io.Writer
	interactive bool
}

type task struct {
	out   io.Writer
	label string
	start time.Time
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex

	message     string
	frame       int
	lineLen     int
	interactive bool
}

func newSpinner(out io.Writer) spinner {
	if out == nil {
		out = io.Discard
	}
	return spinner{out: out, interactive: isInteractive(out)}
}

func (p spinner) Start(label string) *task {
	t := &task{out: p.out, label: label, start: time.Now(), done: make(chan struct{}), interactive: p.interactive}
	t.render()
	go t.track()
	return t
}

func (t *task) Update(message string) {
	if t == nil || message == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if message == t.message {
		return
	}
	t.message = message
	t.renderLocked()
}

func (t *task) Done(err error) {
	if t == nil {
		return
	}
	t.once.Do(func() { close(t.done) })
	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := time.Since(t.start).Round(time.Second)
	if err != nil {
		t.writeLineLocked(fmt.Sprintf("%s failed after %s", t.label, elapsed), true)
		return
	}
	t.writeLineLocked(fmt.Sprintf("%s done in %s", t.label, elapsed), true)
}

func (t *task) track() {
	if !t.interactive {
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.render()
		case <-t.done:
			return
		}
	}
}

func (t *task) render() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.renderLocked()
}

func (t *task) renderLocked() {
	if !t.interactive && t.message != "" {
		t.writeLineLocked("  "+t.message, false)
		return
	}
	frames := [...]string{"-", "\\", "|", "/"}
	var b strings.Builder
	b.WriteString(t.label)
	if t.message != "" {
		b.WriteString(": ")
		b.WriteString(t.message)
	}
	if t.interactive {
		b.WriteByte(' ')
		b.WriteString(frames[t.frame%len(frames)])
		t.frame++
	} else {
		b.WriteString(" ...")
	}
	t.writeLineLocked(b.String(), false)
}

func (t *task) writeLineLocked(line string, newline bool) {
	if !t.interactive {
		fmt.Fprintln(t.out, line)
		return
	}
	pad := ""
	if t.lineLen > len(line) {
		pad = strings.Repeat(" ", t.lineLen-len(line))
	}
	if newline {
		fmt.Fprintf(t.out, "\r%s%s\n", line, pad)
		t.lineLen = 0
		return
	}
	fmt.Fprintf(t.out, "\r%s%s", line, pad)
	t.lineLen = len(line)
}

func isInteractive(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

var stderrSpinner = newSpinner(os.Stderr)

func withProgress(label string, fn func() error) error {
	task := stderrSpinner.Start(label)
	err := fn()
	task.Done(err)
	return err
}
