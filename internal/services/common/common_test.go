package common

import (
	"testing"
	"time"
)

func TestParseLastBuildDate_RoundTripsAcrossZones(t *testing.T) {
	zones := []*time.Location{
		time.UTC,
		time.FixedZone("+0545", 5*3600+45*60),
		time.FixedZone("+0630", 6*3600+30*60),
		time.FixedZone("-0930", -(9*3600 + 30*60)),
		time.FixedZone("EDT", -4*3600),
	}

	for _, loc := range zones {
		now := time.Now().In(loc).Truncate(time.Second)

		parsed, err := ParseLastBuildDate(FormatLastBuildDate(now))
		if err != nil {
			t.Errorf("zone %s: round trip failed: %v", loc, err)
			continue
		}
		if !parsed.Equal(now) {
			t.Errorf("zone %s: got %v, want %v", loc, parsed, now)
		}
	}
}

func TestParseLastBuildDate_AcceptsLegacyRFC1123(t *testing.T) {
	legacy := "Mon, 18 Aug 2026 22:06:52 UTC"

	parsed, err := ParseLastBuildDate(legacy)
	if err != nil {
		t.Fatalf("expected legacy RFC1123 value to parse: %v", err)
	}

	want := time.Date(2026, time.August, 18, 22, 6, 52, 0, time.UTC)
	if !parsed.Equal(want) {
		t.Fatalf("got %v, want %v", parsed, want)
	}
}

func TestParseLastBuildDate_RejectsGarbage(t *testing.T) {
	if _, err := ParseLastBuildDate("not a date"); err == nil {
		t.Fatal("expected an error for an unparseable value")
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"PT2H56M18S", 2*time.Hour + 56*time.Minute + 18*time.Second, true},
		{"PT41M5S", 41*time.Minute + 5*time.Second, true},
		{"PT1H", time.Hour, true},
		{"PT0S", 0, true},
		// YouTube reports these for upcoming premieres / unprocessed lives —
		// the old parser errored on them on every feed refresh
		{"P0D", 0, true},
		{"P1DT2H", 26 * time.Hour, true},
		{"P1W", 7 * 24 * time.Hour, true},
		{"garbage", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if c.ok && err != nil {
			t.Errorf("ParseDuration(%q) unexpected error: %v", c.in, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("ParseDuration(%q) expected error, got %v", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
