package adtext

import "testing"

func seg(a, b float64, t string) Segment { return Segment{Start: a, End: b, Text: t} }

// The real transcript of VGC episode 191. The ad runs 13.5s-35.8s; the mention
// at 7:29 is ordinary conversation and must survive.
func realVGC() []Segment {
	return []Segment{
		seg(0, 7.7, "It's so confusing sometimes to be a gamer. This is VGC the video game podcast episode 191"),
		seg(7.7, 13.5, "with me Jordan Middler, Chris Scullion and Andy Robinson. This week fourteen hundred pounds"),
		seg(13.5, 19.1, "for a PlayStation 5, a circus of value. But first if you want an ad free version of this"),
		seg(19.1, 24.9, "podcast head to patreon.com/videogameschronicle for the ad free video feed you can join"),
		seg(24.9, 29.2, "for as little as three pounds a month, you'll get ad free browsing and ad free video"),
		seg(29.2, 35.8, "and much more at our higher tiers you'll also get extra podcasts and more video content."),
		seg(35.8, 41.0, "How's it going folks Andy Robinson, Mr Chicken Rings himself, how are you doing"),
		seg(449.5, 456.0, "we did for Forgotten Worlds which is still on the Patreon, like a shorter version"),
		seg(456.0, 462.0, "yeah that one was good, I enjoyed recording that one a lot actually"),
	}
}

func TestFindsTheHostReadAd(t *testing.T) {
	spans := FindAds(realVGC(), 4200, DefaultOptions())
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want exactly 1: %v", len(spans), Describe(spans))
	}
	s := spans[0]
	if s.Start > 14 || s.End < 35 {
		t.Fatalf("span %.1f-%.1f does not cover the ad at 13.5-35.8", s.Start, s.End)
	}
	// It must not swallow the show either side.
	if s.Start < 7 || s.End > 42 {
		t.Fatalf("span %.1f-%.1f spills into the show", s.Start, s.End)
	}
}

// The whole point: "still on the Patreon" is the hosts talking, not an ad.
func TestIgnoresAPassingSponsorMention(t *testing.T) {
	for _, sp := range FindAds(realVGC(), 4200, DefaultOptions()) {
		if sp.Start > 400 {
			t.Fatalf("cut show content at %.0fs: %q", sp.Start, sp.Reason)
		}
	}
}

func TestIgnoresSponsorWordWithoutACallToAction(t *testing.T) {
	segs := []Segment{
		seg(0, 10, "we were talking about the sponsor of that tournament the other day"),
		seg(10, 20, "yeah their merch was everywhere, it was a bit much honestly"),
		seg(20, 30, "anyway back to the game we were discussing before the break"),
	}
	if got := FindAds(segs, 1800, DefaultOptions()); len(got) != 0 {
		t.Fatalf("cut conversation with no call to action: %v", Describe(got))
	}
}

// If the rules light up over a big share of the episode they are wrong, and
// losing the show is worse than keeping the ads.
func TestRefusesToCutTooMuch(t *testing.T) {
	var segs []Segment
	for i := 0; i < 40; i++ {
		at := float64(i) * 10
		segs = append(segs, seg(at, at+10, "head to patreon.com/x for the ad free version, three pounds a month"))
	}
	if got := FindAds(segs, 400, DefaultOptions()); len(got) != 0 {
		t.Fatalf("should have refused, got %v", Describe(got))
	}
}

func TestTimestampParsing(t *testing.T) {
	if v := hhmmss("00:01:02,340"); v < 62.33 || v > 62.35 {
		t.Fatalf("got %v, want 62.34", v)
	}
}
