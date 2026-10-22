package sponsorblock

import "testing"

// The real numbers measured across the library on 22 Oct.
func TestRemoved(t *testing.T) {
	cases := []struct {
		name            string
		file, video, of float64
		n               int
		want            Outcome
	}{
		// Kinda Funny Games Daily: file is LONGER than the duration the
		// submitters saw, so yt-dlp discarded every segment and left the ads.
		{"kinda funny games daily", 5064, 5029, 173, 3, NotApplied},
		// Gamescast and Easy Allies: shorter by exactly what was offered.
		{"kinda funny gamescast", 3953, 4128, 175, 3, Applied},
		{"easy allies", 7517, 7600, 83, 1, Applied},
		// No submissions at all.
		{"no segments", 10984, 0, 0, 0, NoSegments},
		// Neither length fits: do not touch the audio.
		{"unclear", 4000, 5000, 100, 2, Unclear},
	}
	for _, c := range cases {
		got, removed := Removed(c.file, c.video, c.of, c.n)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
		if got == Applied && removed != c.of {
			t.Errorf("%s: applied should report the offered time, got %.0f", c.name, removed)
		}
		if got == NotApplied && removed != 0 {
			t.Errorf("%s: nothing removed should report 0, got %.0f", c.name, removed)
		}
	}
}
