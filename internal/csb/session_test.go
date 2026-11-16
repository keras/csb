package csb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var testSessions = []SessionInfo{
	{ID: "aaa111222333", Name: "csb-proj-aaaaaa", Workspace: "/home/me/proj", ConfigDir: "/home/me/.config/csb"},
	{ID: "aab444555666", Name: "csb-other-bbbbbb", Workspace: "/home/me/other", ConfigDir: "/home/me/.config/csb"},
	{ID: "ccc777888999", Name: "csb-proj-cccccc", Workspace: "/home/me/proj", ConfigDir: "/elsewhere/csb"},
}

func TestSessionsForWorkspace(t *testing.T) {
	ws := "/home/me/proj"
	cfg := &Config{ConfigDir: "/home/me/.config/csb", Workspace: &ws}

	got := sessionsForWorkspace(testSessions, cfg)
	if assert.Len(t, got, 1, "same workspace under a different config dir must not match") {
		assert.Equal(t, "csb-proj-aaaaaa", got[0].Name)
	}

	// No workspace: never auto-selected.
	assert.Empty(t, sessionsForWorkspace(testSessions, &Config{ConfigDir: "/home/me/.config/csb"}))
}

func TestMatchSessionTarget(t *testing.T) {
	// Exact name.
	got := matchSessionTarget(testSessions, "csb-other-bbbbbb")
	if assert.Len(t, got, 1) {
		assert.Equal(t, "aab444555666", got[0].ID)
	}

	// Unique ID prefix.
	got = matchSessionTarget(testSessions, "ccc")
	if assert.Len(t, got, 1) {
		assert.Equal(t, "csb-proj-cccccc", got[0].Name)
	}

	// Ambiguous ID prefix.
	assert.Len(t, matchSessionTarget(testSessions, "aa"), 2)

	// No match.
	assert.Empty(t, matchSessionTarget(testSessions, "zzz"))
}

func TestSessionTable(t *testing.T) {
	out := sessionTable([]SessionInfo{
		{Name: "csb-proj-aaaaaa", Workspace: "/home/me/proj", Status: "Up 2 minutes"},
		{Name: "csb-x-bbbbbb", Status: "Up 1 hour"},
	}, "/home/me")
	assert.Contains(t, out, "csb-proj-aaaaaa  ~/proj")
	assert.Contains(t, out, "(no workspace)")
}
