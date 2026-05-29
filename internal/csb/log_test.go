package csb

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── cliHandler ───────────────────────────────────────────────────────────────

func TestCLIHandler_Format(t *testing.T) {
	var buf bytes.Buffer
	h := &cliHandler{w: &buf}
	rec := slog.NewRecord(time.Time{}, slog.LevelInfo, "hello", 0)
	rec.AddAttrs(slog.String("key", "val"))
	require.NoError(t, h.Handle(context.Background(), rec))
	out := buf.String()
	assert.Contains(t, out, "[csb] hello")
	assert.Contains(t, out, "key=val")
}

func TestCLIHandler_Enabled(t *testing.T) {
	h := &cliHandler{w: &bytes.Buffer{}}
	assert.True(t, h.Enabled(context.Background(), slog.LevelInfo))
}

func TestCLIHandler_WithAttrs(t *testing.T) {
	h := &cliHandler{w: &bytes.Buffer{}}
	assert.Equal(t, h, h.WithAttrs(nil))
}

func TestCLIHandler_WithGroup(t *testing.T) {
	h := &cliHandler{w: &bytes.Buffer{}}
	assert.Equal(t, h, h.WithGroup("g"))
}

// ── discardHandler ───────────────────────────────────────────────────────────

func TestDiscardHandler_Enabled(t *testing.T) {
	assert.False(t, discardHandler{}.Enabled(context.Background(), slog.LevelInfo))
}

func TestDiscardHandler_Handle(t *testing.T) {
	require.NoError(t, discardHandler{}.Handle(context.Background(), slog.Record{}))
}

func TestDiscardHandler_WithAttrs(t *testing.T) {
	_, ok := discardHandler{}.WithAttrs(nil).(discardHandler)
	assert.True(t, ok)
}

func TestDiscardHandler_WithGroup(t *testing.T) {
	_, ok := discardHandler{}.WithGroup("g").(discardHandler)
	assert.True(t, ok)
}

// ── InitLogger / logInfo ──────────────────────────────────────────────────────

func TestInitLogger_VerboseTrue(t *testing.T) {
	InitLogger(true)
	assert.NotNil(t, logger)
}

func TestInitLogger_VerboseFalse(t *testing.T) {
	InitLogger(false)
	assert.NotNil(t, logger)
}

func TestLogInfo_NilLogger(t *testing.T) {
	orig := logger
	logger = nil
	defer func() { logger = orig }()
	// Should not panic
	logInfo("test", "k", "v")
}

func TestLogInfo_WithLogger(t *testing.T) {
	var buf bytes.Buffer
	logger = slog.New(&cliHandler{w: &buf})
	defer func() { logger = nil }()
	logInfo("mytestmsg", "foo", "bar")
	assert.True(t, strings.Contains(buf.String(), "mytestmsg"))
}
