package logs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Tee returns a handler that writes through h and also publishes every record it writes to the hub, as a sidecar
// line ("message key=value ..."), for the log viewer.
func Tee(h slog.Handler, hub *Hub) slog.Handler { return &tee{inner: h, hub: hub} }

type tee struct {
	inner  slog.Handler
	hub    *Hub
	prefix string // attributes added with WithAttrs, already formatted
	group  string
}

func (t *tee) Enabled(ctx context.Context, l slog.Level) bool { return t.inner.Enabled(ctx, l) }

func (t *tee) Handle(ctx context.Context, r slog.Record) error {
	err := t.inner.Handle(ctx, r)
	var b strings.Builder
	b.WriteString(printable(r.Message))
	b.WriteString(t.prefix)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, t.group, a)
		return true
	})
	text := b.String()
	if len(text) > maxText {
		text = text[:maxText]
	}
	t.hub.Publish(Line{T: r.Time.UnixMilli(), Source: Sidecar, Level: levelName(r.Level), Text: text})
	return err
}

func (t *tee) WithAttrs(as []slog.Attr) slog.Handler {
	var b strings.Builder
	for _, a := range as {
		writeAttr(&b, t.group, a)
	}
	return &tee{inner: t.inner.WithAttrs(as), hub: t.hub, prefix: t.prefix + b.String(), group: t.group}
}

func (t *tee) WithGroup(name string) slog.Handler {
	return &tee{inner: t.inner.WithGroup(name), hub: t.hub, prefix: t.prefix, group: t.group + name + "."}
}

func writeAttr(b *strings.Builder, group string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			writeAttr(b, group+a.Key+".", g)
		}
		return
	}
	// Quoted (escaped) when it would not read as one value on one line: values can hold client-chosen text, such as
	// the user name of a denied authentication.
	s := v.String()
	if strings.ContainsAny(s, " \"=") || strings.ContainsFunc(s, notPrint) || s == "" {
		s = fmt.Sprintf("%q", s)
	}
	b.WriteString(" " + group + a.Key + "=" + s)
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
