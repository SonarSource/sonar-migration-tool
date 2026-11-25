// Copyright (C) SonarSource Sàrl
// For more information, see https://sonarsource.com/legal/
// mailto:info AT sonarsource DOT com

package redact

import (
	"context"
	"log/slog"
)

// handler keeps two parallel inner handlers: raw carries the attributes and
// groups as supplied, red carries their redacted form. Handle picks one per
// record, so the switch can flip after WithAttrs/WithGroup were called.
type handler struct {
	raw slog.Handler
	red slog.Handler
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.raw.Enabled(ctx, l)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &handler{raw: h.raw.WithAttrs(attrs), red: h.red.WithAttrs(redactAttrs(attrs))}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{raw: h.raw.WithGroup(name), red: h.red.WithGroup(name)}
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if !Enabled() {
		return h.raw.Handle(ctx, r)
	}
	nr := slog.NewRecord(r.Time, r.Level, maskEmailsInText(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.red.Handle(ctx, nr)
}

func redactAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return out
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redactAttrs(v.Group())...)}
	case slog.KindString:
		return slog.String(a.Key, redactString(a.Key, v.String(), ""))
	case slog.KindAny:
		return redactAny(a.Key, v.Any())
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func redactAny(key string, x any) slog.Attr {
	switch t := x.(type) {
	case error:
		return slog.String(key, redactString(key, errorText(t), ""))
	case string:
		return slog.String(key, redactString(key, t, ""))
	}
	return slog.Any(key, x)
}

// errorText renders err, tolerating typed-nil errors whose Error method
// dereferences a nil receiver.
func errorText(err error) (text string) {
	defer func() {
		if recover() != nil {
			text = "<nil>"
		}
	}()
	return err.Error()
}
