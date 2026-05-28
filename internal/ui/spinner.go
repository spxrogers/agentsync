package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerInterval is the animation frame rate. 100ms ≈ 10Hz, slow enough that
// the CPU cost is negligible and fast enough to feel live.
const spinnerInterval = 100 * time.Millisecond

// spinnerFrames is a 10-step braille cycle — single-display-column, supported
// by every modern terminal, and visually consistent with the rest of the glyph
// vocabulary.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner is a lightweight in-place progress indicator for slow network ops
// (marketplace fetch, plugin pull). It animates only when its writer is a
// terminal; off a terminal (CI logs, piped stderr, captured-output tests) it
// is a complete no-op — no animation, no static fallback line — so byte-stable
// fixtures stay byte-stable and grep'd output stays clean. The success line a
// caller already prints carries the result.
type Spinner struct {
	w       io.Writer
	label   string
	animate bool
	color   bool

	mu      sync.Mutex
	started bool
	stopped bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// Spinner builds a Spinner bound to p.Err and inheriting p's color decision.
// Call Start to begin and Stop to end; both are idempotent and safe to call
// in either order (a Stop without Start is a no-op).
func (p *Printer) Spinner(label string) *Spinner {
	return &Spinner{
		w:       p.Err,
		label:   label,
		animate: isTerminal(p.Err),
		color:   p.color,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// Spin is the one-call helper: it starts a Spinner and returns the stop
// function. Typical use:
//
//	stop := p.Spin("fetching marketplace github")
//	result, err := fetcher.Fetch(src, cacheDir)
//	stop()
//	if err != nil { ... }
func (p *Printer) Spin(label string) func() {
	s := p.Spinner(label)
	s.Start()
	return s.Stop
}

// Start begins the animation. Idempotent; no-op on a non-terminal writer.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	if !s.animate {
		return
	}
	go s.loop()
}

func (s *Spinner) loop() {
	defer close(s.doneCh)
	t := time.NewTicker(spinnerInterval)
	defer t.Stop()
	i := 0
	for {
		select {
		case <-s.stopCh:
			// Erase the spinner line (\r to col 0, \x1b[K to clear to EOL) so the
			// caller's next write starts on a fresh, blank line.
			fmt.Fprint(s.w, "\r\x1b[K")
			return
		case <-t.C:
			frame := spinnerFrames[i%len(spinnerFrames)]
			if s.color {
				fmt.Fprintf(s.w, "\r%s%s%s %s", codeCyan, frame, codeReset, s.label)
			} else {
				fmt.Fprintf(s.w, "\r%s %s", frame, s.label)
			}
			i++
		}
	}
}

// Stop ends the animation, clears the spinner line, and blocks until the
// animation goroutine has flushed. Idempotent; safe to call before Start
// (in which case it is a no-op).
func (s *Spinner) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	started := s.started
	s.mu.Unlock()
	if !s.animate || !started {
		return
	}
	close(s.stopCh)
	<-s.doneCh
}
