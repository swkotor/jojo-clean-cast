package common

import (
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

func ParseDuration(durationStr string) (time.Duration, error) {
	// Remove the 'PT' prefix from the duration string
	durationStr = strings.Replace(durationStr, "PT", "", 1)

	// Replace 'H' with 'h', 'M' with 'm', and 'S' with 's'
	durationStr = strings.Replace(durationStr, "H", "h", 1)
	durationStr = strings.Replace(durationStr, "M", "m", 1)
	durationStr = strings.Replace(durationStr, "S", "s", 1)

	// Parse the duration string
	return time.ParseDuration(durationStr)
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
