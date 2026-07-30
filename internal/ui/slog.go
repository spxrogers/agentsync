package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// SlogHandler renders slog records through the diagnostic vocabulary in diag.go,
// so a library-side slog.Warn is indistinguishable from a command's p.Warnf.
//
// Without it, the render and marketplace pipelines' slog.Warn calls fall through
// to slog's default and print through the standard log package —
// `2026/07/28 15:03:45 WARN …`, a shape nothing else in the CLI uses. See diag.go
// for why that mattered.
//
// Records render as the level label, the message, then any attributes on a
// continuation line indented to the message column:
//
//	⚠ WARN   plugin component frontmatter is not strict YAML; parsed leniently
//	         path=/Users/me/.agentsync/.state/cache/plugins/x/agents/y.md
//
// Attributes go on their own line because the ones agentsync logs are paths and
// wrapped errors — long enough that appending them inline pushes the message
// itself off the screen.
type SlogHandler struct {
	p     *Printer
	w     io.Writer
	level slog.Leveler
	// mu serializes writes to w. slog.Handler implementations are expected to be
	// safe for concurrent use — the stdlib's own handlers lock their writer — and
	// callers reasonably assume a *slog.Logger can be shared across goroutines.
	// A record here renders as a label line plus an optional attribute line, so
	// without the lock two concurrent records could interleave and split an
	// attribute line away from the diagnostic it belongs to.
	//
	// SHARED by derivation: clone() copies the pointer, so every handler derived
	// via WithAttrs/WithGroup locks the SAME mutex as its parent. That is required
	// for correctness — they all write to the same w, and a per-clone mutex would
	// serialize nothing.
	mu *sync.Mutex
	// attrs are the accumulated WithAttrs attributes, already prefixed with any
	// enclosing group path. Held pre-rendered as key=value strings: the handler
	// only ever formats them, never inspects them.
	attrs []string
	// groups is the open WithGroup path, joined with "." to qualify keys.
	groups []string
}

// NewSlogHandler returns a handler that writes records to w styled by p at or
// above level. Pass p.Err as w for the normal case; the writer is explicit so a
// caller can tee logs somewhere else without restyling them.
func NewSlogHandler(w io.Writer, p *Printer, level slog.Leveler) *SlogHandler {
	return &SlogHandler{p: p, w: w, level: level, mu: new(sync.Mutex)}
}

// Enabled implements slog.Handler.
func (h *SlogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

// WithAttrs implements slog.Handler, returning a handler that prepends attrs to
// every record. The receiver is left untouched (slog requires handlers be safe
// to derive from concurrently).
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	out := h.clone()
	for _, a := range attrs {
		out.attrs = append(out.attrs, renderAttr(h.groups, a)...)
	}
	return out
}

// WithGroup implements slog.Handler. An empty name is a no-op per the contract.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := h.clone()
	out.groups = append(out.groups, name)
	return out
}

func (h *SlogHandler) clone() *SlogHandler {
	return &SlogHandler{
		p:     h.p,
		w:     h.w,
		level: h.level,
		mu:    h.mu, // deliberately shared — see the field doc
		// Copy rather than share the backing array: two handlers derived from
		// the same parent must not have one's append clobber the other's tail.
		attrs:  append([]string(nil), h.attrs...),
		groups: append([]string(nil), h.groups...),
	}
}

// Handle implements slog.Handler.
//
// The record's time is deliberately dropped. agentsync's slog output is
// user-facing CLI diagnostics, not a log stream anyone greps by timestamp, and
// a wall clock on every warning was the single loudest thing wrong with the
// pre-existing output. Nothing here feeds a content hash or the drift
// classifier, so dropping it costs nothing downstream.
func (h *SlogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := append([]string(nil), h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, renderAttr(h.groups, a)...)
		return true
	})

	// Sanitize: slog messages and attributes routinely carry filesystem paths
	// and error strings harvested from third-party agent config, which is
	// untrusted text headed for a terminal (#93/#171). Note Sanitize STRIPS
	// newlines rather than escaping them, so a multi-line slog message collapses
	// to one line — acceptable here because a log record's Message is a single
	// line by convention, and the alternative (indenting attacker-chosen line
	// breaks into the message column) would let a crafted message forge what
	// looks like a second, separate diagnostic.
	style := h.p.styleForDiag(h.w)
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", levelOf(r.Level).Label(style), Sanitize(r.Message))
	if len(attrs) > 0 {
		fmt.Fprintf(&b, "%s%s\n", diagIndent, style.Faint(Sanitize(strings.Join(attrs, " "))))
	}

	// ONE Write under the lock: the label line and its attribute line are a
	// single logical diagnostic and must not be split by a concurrent record.
	h.mu.Lock()
	defer h.mu.Unlock()
	// Return the write error rather than swallowing it. slog's own Logger
	// discards what Handle returns, so this changes nothing for a live CLI — but
	// a handler that reports "success" after a failed write is a lie that makes
	// a tee'd or piped sink impossible to debug, and a test can assert on it.
	_, err := io.WriteString(h.w, b.String())
	return err
}

// levelOf maps a slog.Level onto the CLI's four-level vocabulary. slog levels
// are an open integer scale, so anything at or above Error is an error and
// anything below Info is debug — a custom intermediate level lands on the
// nearest label below it rather than being dropped.
func levelOf(l slog.Level) Level {
	switch {
	case l >= slog.LevelError:
		return LevelError
	case l >= slog.LevelWarn:
		return LevelWarn
	case l >= slog.LevelInfo:
		return LevelInfo
	default:
		return LevelDebug
	}
}

// renderAttr flattens one attribute into zero or more `key=value` strings,
// qualifying keys with the enclosing group path. Group-valued attrs recurse, an
// empty Attr is dropped per the slog.Handler contract, and a value containing a
// space is quoted so the continuation line stays parseable by eye.
func renderAttr(groups []string, a slog.Attr) []string {
	a.Value = a.Value.Resolve() // run any LogValuer before inspecting the kind
	if a.Equal(slog.Attr{}) {
		return nil
	}
	if a.Value.Kind() == slog.KindGroup {
		inner := a.Value.Group()
		if len(inner) == 0 {
			return nil // an empty group is elided, key and all
		}
		sub := groups
		if a.Key != "" {
			sub = append(append([]string(nil), groups...), a.Key)
		}
		var out []string
		for _, g := range inner {
			out = append(out, renderAttr(sub, g)...)
		}
		return out
	}
	key := a.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	val := a.Value.String()
	if strings.ContainsAny(val, " \t") {
		val = fmt.Sprintf("%q", val)
	}
	return []string{key + "=" + val}
}
