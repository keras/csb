package csb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ── addonNames ───────────────────────────────────────────────────────────────

func TestAddonNames_Empty(t *testing.T) {
	cfg := &Config{}
	cfg.Addons = []string{}
	assert.Equal(t, "none", addonNames(cfg))
}

func TestAddonNames_Multiple(t *testing.T) {
	cfg := &Config{}
	cfg.Addons = []string{"mise", "sudo", "packages git"}
	assert.Equal(t, "mise,sudo,packages", addonNames(cfg))
}

func TestAddonNames_SingleWithArgs(t *testing.T) {
	cfg := &Config{}
	cfg.Addons = []string{"packages git nano"}
	assert.Equal(t, "packages", addonNames(cfg))
}

// ── shortenHome ──────────────────────────────────────────────────────────────

func TestShortenHome_ExactHome(t *testing.T) {
	assert.Equal(t, "~", shortenHome("/home/user", "/home/user"))
}

func TestShortenHome_BelowHome(t *testing.T) {
	assert.Equal(t, "~/.config/csb", shortenHome("/home/user/.config/csb", "/home/user"))
}

func TestShortenHome_Outside(t *testing.T) {
	assert.Equal(t, "/etc/csb", shortenHome("/etc/csb", "/home/user"))
}

func TestShortenHome_EmptyPath(t *testing.T) {
	assert.Equal(t, "", shortenHome("", "/home/user"))
}

func TestShortenHome_EmptyHome(t *testing.T) {
	assert.Equal(t, "/some/path", shortenHome("/some/path", ""))
}

// ── padRight ─────────────────────────────────────────────────────────────────

func TestPadRight_ShortString(t *testing.T) {
	assert.Equal(t, "hi   ", padRight("hi", 5))
}

func TestPadRight_ExactWidth(t *testing.T) {
	assert.Equal(t, "hello", padRight("hello", 5))
}

func TestPadRight_LongerThanWidth(t *testing.T) {
	assert.Equal(t, "toolong", padRight("toolong", 3))
}

// ── imageRepoTag ─────────────────────────────────────────────────────────────

func TestImageRepoTag_WithTag(t *testing.T) {
	img := ImageInfo{Repository: "csb", Tag: "abc123def456"}
	assert.Equal(t, "csb:abc123def456", imageRepoTag(img))
}

func TestImageRepoTag_NoTag(t *testing.T) {
	img := ImageInfo{Repository: "csb", Tag: ""}
	assert.Equal(t, "csb", imageRepoTag(img))
}

func TestImageRepoTag_NoneTag(t *testing.T) {
	img := ImageInfo{Repository: "csb", Tag: "<none>"}
	assert.Equal(t, "csb", imageRepoTag(img))
}

func TestImageRepoTag_NoneRepo(t *testing.T) {
	img := ImageInfo{Repository: "<none>", Tag: ""}
	assert.Equal(t, "<none>", imageRepoTag(img))
}

func TestImageRepoTag_EmptyRepo(t *testing.T) {
	img := ImageInfo{Repository: "", Tag: "abc"}
	// empty repo → "<none>", tag present → "<none>:abc"
	assert.Equal(t, "<none>:abc", imageRepoTag(img))
}

// ── humanAge ─────────────────────────────────────────────────────────────────

func TestHumanAge_JustNow(t *testing.T) {
	assert.Equal(t, "just now", humanAge(time.Now().Add(-10*time.Second)))
}

func TestHumanAge_OneMinuteAgo(t *testing.T) {
	assert.Equal(t, "1 minute ago", humanAge(time.Now().Add(-90*time.Second)))
}

func TestHumanAge_MinutesAgo(t *testing.T) {
	assert.Equal(t, "5 minutes ago", humanAge(time.Now().Add(-5*time.Minute)))
}

func TestHumanAge_OneHourAgo(t *testing.T) {
	assert.Equal(t, "1 hour ago", humanAge(time.Now().Add(-90*time.Minute)))
}

func TestHumanAge_HoursAgo(t *testing.T) {
	assert.Equal(t, "3 hours ago", humanAge(time.Now().Add(-3*time.Hour)))
}

func TestHumanAge_OneDayAgo(t *testing.T) {
	assert.Equal(t, "1 day ago", humanAge(time.Now().Add(-36*time.Hour)))
}

func TestHumanAge_DaysAgo(t *testing.T) {
	assert.Equal(t, "3 days ago", humanAge(time.Now().Add(-3*24*time.Hour)))
}

func TestHumanAge_OneWeekAgo(t *testing.T) {
	assert.Equal(t, "1 week ago", humanAge(time.Now().Add(-10*24*time.Hour)))
}

func TestHumanAge_WeeksAgo(t *testing.T) {
	assert.Equal(t, "2 weeks ago", humanAge(time.Now().Add(-16*24*time.Hour)))
}

func TestHumanAge_OneMonthAgo(t *testing.T) {
	assert.Equal(t, "1 month ago", humanAge(time.Now().Add(-45*24*time.Hour)))
}

func TestHumanAge_MonthsAgo(t *testing.T) {
	assert.Equal(t, "3 months ago", humanAge(time.Now().Add(-100*24*time.Hour)))
}

func TestHumanAge_OneYearAgo(t *testing.T) {
	assert.Equal(t, "1 year ago", humanAge(time.Now().Add(-400*24*time.Hour)))
}
