package adapter

import (
	"context"
	"log/slog"

	"github.com/rs/zerolog"

	"github.com/pyck-ai/pyck/backend/common/log"
)

// SlogAdapter adapts a zerolog logger to a structured slog logger.
func SlogAdapter(zl log.Logger) *slog.Logger {
	logger := zl.With().
		Str("log.adapter", "slog").
		Logger()

	return slog.New(&slogHandler{logger: logger})
}

// slogHandler implements slog.Handler on top of a zerolog logger. The record
// context is attached to every event so zerolog hooks reading e.GetCtx()
// (trace, request-id) keep working for slog-originated log lines.
type slogHandler struct {
	logger log.Logger
	attrs  []slog.Attr
	groups []string
}

func (h *slogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return log.Enabled(h.logger, zerologLevel(level))
}

func (h *slogHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := map[string]any{}
	for _, attr := range h.attrs {
		appendAttr(fields, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		appendAttr(fields, qualify(h.groups, attr))
		return true
	})

	h.logger.WithLevel(zerologLevel(record.Level)).
		Ctx(ctx).
		Fields(fields).
		Msg(record.Message)
	return nil
}

func (h *slogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	nh.attrs = append(nh.attrs, h.attrs...)
	for _, attr := range attrs {
		nh.attrs = append(nh.attrs, qualify(h.groups, attr))
	}
	return &nh
}

func (h *slogHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.groups = append(append([]string{}, h.groups...), name)
	return &nh
}

// qualify nests attr under the currently open slog groups.
func qualify(groups []string, attr slog.Attr) slog.Attr {
	for i := len(groups) - 1; i >= 0; i-- {
		attr = slog.Group(groups[i], attr)
	}
	return attr
}

// appendAttr resolves attr into fields, expanding groups into nested maps and
// merging groups that share a key so repeated qualification does not clobber
// previously added fields.
func appendAttr(fields map[string]any, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()

	if attr.Value.Kind() == slog.KindGroup {
		groupAttrs := attr.Value.Group()
		if len(groupAttrs) == 0 {
			return
		}
		// Inline groups with empty keys, per slog.Handler contract.
		if attr.Key == "" {
			for _, a := range groupAttrs {
				appendAttr(fields, a)
			}
			return
		}
		nested, ok := fields[attr.Key].(map[string]any)
		if !ok {
			nested = map[string]any{}
			fields[attr.Key] = nested
		}
		for _, a := range groupAttrs {
			appendAttr(nested, a)
		}
		return
	}

	if attr.Key == "" {
		return
	}
	fields[attr.Key] = attr.Value.Any()
}

func zerologLevel(level slog.Level) zerolog.Level {
	switch {
	case level >= slog.LevelError:
		return zerolog.ErrorLevel
	case level >= slog.LevelWarn:
		return zerolog.WarnLevel
	case level >= slog.LevelInfo:
		return zerolog.InfoLevel
	default:
		return zerolog.DebugLevel
	}
}
