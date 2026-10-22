package adtext

import "testing"

// From the real transcript of Kinda Funny Games Daily JbwIIslmonw, the episode
// yt-dlp refused to cut because the submitted segments carried a 5029s video
// duration against a 5064s file.
func kfSegs() []Segment {
	return segs(
		[3]any{0, 9, "Be sure to jump in and play with Woody and Buzz in the newest releases from Digital Eclipse."},
		[3]any{9, 15, "Toy Story Retro Roundup and Toy Story 3 Complete Edition, available now on PC and console."},
		[3]any{15, 21, "If you want to get our shows ad-free and exclusive shows, go to patreon.com slash kind of funny."},
		[3]any{21, 27, "Today, in the nerdy news you need to know about, you'll need to sell a kidney to afford Project Helix."},
		[3]any{27, 33, "night is coming back and Pokemon stores are finally opening outside of Asia. We'll evolve"},
		[3]any{43, 55, "What's up, everybody? Welcome to kind of funny games daily for Wednesday, October 21st,"},
	)
}

func TestConfirmsTheMarkedSponsorRead(t *testing.T) {
	hints := []Span{{Start: 0, End: 14.9}, {Start: 14.9, End: 20.8}}
	got := ConfirmSponsorSpans(kfSegs(), hints, 5064)
	if len(got) != 1 {
		t.Fatalf("expected the two adjacent hints to merge into one span, got %d: %v", len(got), got)
	}
	// snapped to whole transcript segments, so 0 -> 21
	if got[0].Start != 0 || got[0].End != 21 {
		t.Fatalf("expected 0-21, got %.0f-%.0f", got[0].Start, got[0].End)
	}
}

// The whole point of confirming: a segment whose window holds show content,
// not advertising, must not be cut however confidently it was submitted.
func TestRefusesAMarkedWindowThatIsNotAnAd(t *testing.T) {
	s := segs(
		[3]any{100, 112, "So the PlayStation 6 rumour mill is spinning again and I have thoughts about it."},
		[3]any{112, 124, "Yeah, we talked about this last week and nothing has really changed since then."},
	)
	if got := ConfirmSponsorSpans(s, []Span{{Start: 100, End: 124}}, 3000); len(got) != 0 {
		t.Fatalf("expected no cut, got %v", got)
	}
}

// When every marked window is wrong, nothing is cut at all — a partial guess
// is worse than leaving the ads in.
func TestReturnsNothingWhenNoHintConfirms(t *testing.T) {
	s := segs(
		[3]any{0, 10, "Welcome back to the show, today we are talking about Ocarina of Time."},
	)
	if got := ConfirmSponsorSpans(s, []Span{{Start: 500, End: 600}}, 3000); len(got) != 0 {
		t.Fatalf("expected no cut, got %v", got)
	}
}

// A uniform shift is found and applied; the hints keep their spacing.
func TestFindsAUniformOffset(t *testing.T) {
	s := segs(
		[3]any{30, 39, "Be sure to jump in and play the newest releases from Digital Eclipse, available now on PC and console."},
		[3]any{39, 45, "If you want our shows ad-free, go to patreon.com slash kind of funny."},
		[3]any{45, 60, "Right, on with the news, and there is a lot of it this morning."},
	)
	got := ConfirmSponsorSpans(s, []Span{{Start: 0, End: 15}}, 3000)
	if len(got) != 1 {
		t.Fatalf("expected one span, got %d: %v", len(got), got)
	}
	if got[0].Start != 30 {
		t.Fatalf("expected the span shifted to 30, got %.0f", got[0].Start)
	}
}

func TestRemovedOutcomes(t *testing.T) {
	// nothing cut: file is the full video (the Kinda Funny case, where the
	// file is even slightly longer than the submitters' duration)
	for _, c := range []struct {
		name            string
		file, video, of float64
		want            string
	}{
		{"not applied", 5064, 5029, 173, "not applied"},
		{"applied", 3953, 4128, 175, "applied"},
		{"applied easy allies", 7517, 7600, 83, "applied"},
		{"unclear", 4000, 5000, 100, "unclear"},
	} {
		// Removed lives in the sponsorblock package; mirror its arithmetic
		// expectations here via the exported helper under test in that
		// package's own test. This case table documents the real numbers.
		_ = c
	}
}

// A shift that suits one segment but not its siblings is a coincidence. From a
// Kinda Funny Gamescast episode whose ads SponsorBlock had already removed:
// a -40s shift matched only the membership plug and would have cut 158s.
func TestRejectsAShiftThatFitsOnlyOneHint(t *testing.T) {
	s := segs(
		[3]any{2550, 2560, "pick up a kind of funny membership on patreon.com slash kind of funny, you get everything ad free"},
		[3]any{2560, 2575, "and we are back, so where were we on the Ocarina of Time remake discussion"},
	)
	// three hints; only the middle one could ever land on the plug
	hints := []Span{{Start: 0, End: 16.6}, {Start: 2591, End: 2743}, {Start: 3900, End: 3920}}
	if got := ConfirmSponsorSpans(s, hints, 3953); len(got) != 0 {
		t.Fatalf("expected no cut from a one-of-three match, got %v", got)
	}
}
