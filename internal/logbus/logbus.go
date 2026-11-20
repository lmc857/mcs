// Package logbus provides an in-memory log ring plus live subscribers.
package logbus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

type Hub struct {
	mu      sync.RWMutex
	max     int
	entries []Entry
	subs    map[chan Entry]struct{}
}

func New(max int) *Hub {
	if max <= 0 {
		max = 1000
	}
	return &Hub{max: max, subs: map[chan Entry]struct{}{}}
}

func (h *Hub) Add(e Entry) {
	h.mu.Lock()
	h.entries = append(h.entries, e)
	if len(h.entries) > h.max {
		h.entries = append([]Entry(nil), h.entries[len(h.entries)-h.max:]...)
	}
	h.mu.Unlock()
	h.mu.RLock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
	h.mu.RUnlock()
}

func (h *Hub) Recent() []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]Entry(nil), h.entries...)
}

func (h *Hub) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Handler is a slog.Handler that mirrors records into a Hub.
type Handler struct {
	hub    *Hub
	base   slog.Handler
	attrs  map[string]any
	groups []string
}

func NewHandler(hub *Hub, base slog.Handler) slog.Handler {
	return &Handler{hub: hub, base: base, attrs: map[string]any{}}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func groupPrefix(groups []string) string {
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, ".") + "."
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if h.hub != nil {
		e := Entry{
			Time:    r.Time.Format(time.RFC3339Nano),
			Level:   r.Level.String(),
			Message: r.Message,
			Attrs:   map[string]any{},
		}
		for k, v := range h.attrs {
			e.Attrs[k] = v
		}
		prefix := groupPrefix(h.groups)
		r.Attrs(func(a slog.Attr) bool {
			addAttr(e.Attrs, prefix, a)
			return true
		})
		if len(e.Attrs) == 0 {
			e.Attrs = nil
		}
		h.hub.Add(e)
	}
	return h.base.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &Handler{hub: h.hub, base: h.base.WithAttrs(attrs), attrs: map[string]any{}, groups: append([]string(nil), h.groups...)}
	for k, v := range h.attrs {
		next.attrs[k] = v
	}
	prefix := groupPrefix(h.groups)
	for _, a := range attrs {
		addAttr(next.attrs, prefix, a)
	}
	return next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	next := &Handler{hub: h.hub, base: h.base.WithGroup(name), attrs: map[string]any{}, groups: append(append([]string(nil), h.groups...), name)}
	for k, v := range h.attrs {
		next.attrs[k] = v
	}
	return next
}

func addAttr(dst map[string]any, prefix string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, child := range v.Group() {
			addAttr(dst, prefix+a.Key+".", child)
		}
		return
	}
	var out any
	switch v.Kind() {
	case slog.KindString:
		out = v.String()
	case slog.KindInt64:
		out = v.Int64()
	case slog.KindUint64:
		out = v.Uint64()
	case slog.KindFloat64:
		out = v.Float64()
	case slog.KindBool:
		out = v.Bool()
	case slog.KindDuration:
		out = v.Duration().String()
	case slog.KindTime:
		out = v.Time().Format(time.RFC3339Nano)
	default:
		out = fmt.Sprint(v.Any())
	}
	dst[prefix+a.Key] = out
}
