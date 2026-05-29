package csb

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

var logger *slog.Logger

// InitLogger configures the package-level logger. Call once after parsing args.
// When verbose is false all log calls are no-ops.
func InitLogger(verbose bool) {
	if verbose {
		logger = slog.New(&cliHandler{w: os.Stderr})
	} else {
		logger = slog.New(discardHandler{})
	}
}

func logInfo(msg string, args ...any) {
	if logger != nil {
		logger.Info(msg, args...)
	}
}

// cliHandler formats records as: [csb] message  key=value  key=value
type cliHandler struct{ w io.Writer }

func (h *cliHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *cliHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString("[csb] ")
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString("  ")
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		return true
	})
	b.WriteByte('\n')
	_, err := fmt.Fprint(h.w, b.String())
	return err
}

func (h *cliHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *cliHandler) WithGroup(name string) slog.Handler       { return h }

type discardHandler struct{}

func (discardHandler) Enabled(_ context.Context, _ slog.Level) bool { return false }
func (discardHandler) Handle(_ context.Context, _ slog.Record) error {
	return nil
}
func (discardHandler) WithAttrs(_ []slog.Attr) slog.Handler { return discardHandler{} }
func (discardHandler) WithGroup(_ string) slog.Handler      { return discardHandler{} }
