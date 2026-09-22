package common

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	ytApi "google.golang.org/api/youtube/v3"
)

func Contains(slice []string, item string) bool {
	for _, a := range slice {
		if a == item {
			return true
		}
	}
	return false
}

func CleanPlaylistItems(item *ytApi.PlaylistItem) *ytApi.PlaylistItem {
	unavailableStatuses := map[string]bool{
		"private":                  true,
		"unlisted":                 true,
		"privacyStatusUnspecified": true,
	}
	if item.Status != nil {
		if !unavailableStatuses[item.Status.PrivacyStatus] {
			return item
		}
	}

	return nil
}

func FormatLastBuildDate(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func ParseLastBuildDate(value string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC1123, value)
}

var iso8601DurationRe = regexp.MustCompile(
	`^P(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// ParseDuration parses an ISO-8601 duration as the YouTube API returns it.
// fork: the old implementation only rewrote "PT#H#M#S" into Go syntax, so any
// duration with a date part failed — including "P0D", which YouTube reports
// for upcoming premieres and still-processing live streams. That logged
// `time: invalid duration "P0D"` on every feed refresh, forever, for any
// playlist containing a scheduled premiere.
func ParseDuration(durationStr string) (time.Duration, error) {
	m := iso8601DurationRe.FindStringSubmatch(strings.TrimSpace(durationStr))
	if m == nil {
		return 0, fmt.Errorf("invalid ISO-8601 duration %q", durationStr)
	}
	atoi := func(s string) int {
		if s == "" {
			return 0
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	secs := 0.0
	if m[5] != "" {
		secs, _ = strconv.ParseFloat(m[5], 64)
	}
	d := time.Duration(atoi(m[1])*7*24+atoi(m[2])*24+atoi(m[3]))*time.Hour +
		time.Duration(atoi(m[4]))*time.Minute +
		time.Duration(secs*float64(time.Second))
	return d, nil
}

// SanitizeDirName turns a podcast title into a safe directory name
func SanitizeDirName(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case unicode.IsLetter(c) || unicode.IsNumber(c):
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_' || c == '.' || c == '(' || c == ')' || c == '\'' || c == '&' || c == ',':
			b.WriteRune(c)
		default:
			// drop path separators and other unsafe characters
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func IsValidFilename(filename string) bool {
	for _, c := range filename {
		if !unicode.IsLetter(c) && !unicode.IsNumber(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func IsValidParam(param string) bool {
	if strings.Contains(param, "/") || strings.Contains(param, "\\") || strings.Contains(param, "..") {
		return false
	}
	return true
}

func IsValidID(id string) bool {
	for _, c := range id {
		if !unicode.IsLetter(c) && !unicode.IsNumber(c) && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
