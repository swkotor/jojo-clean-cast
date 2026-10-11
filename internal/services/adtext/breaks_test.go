package adtext

import "testing"

// Built from the real transcript of Game Mess Mornings us5v-YCz-70. The
// sponsor/CTA scoring finds nothing in this show, so without the structural
// break rule the 16s hand-off into the ad break stayed in the episode.
func segs(in ...[3]any) []Segment {
	out := make([]Segment, 0, len(in))
	for _, r := range in {
		out = append(out, Segment{
			Start: float64(r[0].(int)),
			End:   float64(r[1].(int)),
			Text:  r[2].(string),
		})
	}
	return out
}

func TestFindsTheAdBreakHandoff(t *testing.T) {
	s := segs(
		[3]any{1718, 1724, "of AI stories this morning with apologies, but it is it is reached a sort of breaking point,"},
		[3]any{1724, 1729, "I think, and we'll get to that here in a second. Before that though, let's take a break. When we"},
		[3]any{1729, 1733, "come back, we've got plenty of headlines to get into. If you want to skip out on these ad messages,"},
		[3]any{1733, 1738, "you can do so go to giantbomb.com/join and get the ad-free version of the audio and video show"},
		[3]any{1738, 1740, "there. We will be right back."},
		[3]any{1745, 1751, "All right, it's game mass mornings. Let's get back into it."},
	)
	got := FindBreakAnnouncements(s)
	if len(got) != 1 {
		t.Fatalf("expected one span, got %d: %v", len(got), got)
	}
	if got[0].Start != 1724 || got[0].End != 1740 {
		t.Fatalf("expected 1724-1740, got %.0f-%.0f", got[0].Start, got[0].End)
	}
}

// The membership pitch in these shows' opening minutes is interleaved with real
// conversation sentence by sentence. Anything that cut it would take the show
// with it, so it must be left alone.
func TestLeavesTheInterleavedMembershipPitchAlone(t *testing.T) {
	s := segs(
		[3]any{182, 186, "This is sure. A thousand times better. All right, let's explain ourselves. We're a giant bomb."},
		[3]any{186, 192, "Go to giantbomb.com/join become a premium member, support independent games media. We got to like"},
		[3]any{192, 197, "watch ourselves see everybody. We got to watch our own backs because the rest of this industry"},
		[3]any{263, 267, "everybody. So, you know, to watch that, you got to be a premium member. You could do that by"},
		[3]any{267, 271, "going to giantbomb.com/join. Also gets you ad free versions of the shows and a bunch of other"},
		[3]any{271, 276, "cool stuff. Also, you know, you just get to be part of a cool community. As far as this show goes,"},
	)
	if got := FindAds(s, 3374, DefaultOptions()); len(got) != 0 {
		t.Fatalf("expected no cuts in the show intro, got %v", got)
	}
}

// Show business that happens to mention the site is not an ad.
func TestLeavesShowBusinessAlone(t *testing.T) {
	s := segs(
		[3]any{3240, 3245, "meeting even uh so you can go to the vote here button on giant bomb.com and add your game for"},
		[3]any{3245, 3251, "this week so we can have a cool bomb 12 on Friday when we wrap up this week uh speaking of this"},
	)
	if got := FindAds(s, 3374, DefaultOptions()); len(got) != 0 {
		t.Fatalf("expected no cuts, got %v", got)
	}
}

// A break with no ad in it is a break, not an ad break.
func TestIgnoresANonAdBreak(t *testing.T) {
	s := segs(
		[3]any{100, 106, "This is a lot. Honestly, let's take a break, I need to refill my coffee."},
		[3]any{106, 110, "Yeah go ahead, we'll be right back."},
	)
	if got := FindBreakAnnouncements(s); len(got) != 0 {
		t.Fatalf("expected no cuts, got %v", got)
	}
}

// Without a closing cue there is no way to know where the break ends.
func TestIgnoresAnUnterminatedCue(t *testing.T) {
	s := segs(
		[3]any{100, 106, "If you want to skip out on these ad messages, become a member."},
		[3]any{106, 112, "Anyway, on to the next story about a video game."},
	)
	if got := FindBreakAnnouncements(s); len(got) != 0 {
		t.Fatalf("expected no cuts, got %v", got)
	}
}

// From the real transcript of Game Mess Mornings dfMZkd4vQHM, where whisper
// produced 1578 short segments instead of 596 long ones. The cue sentence is
// split across three of them, which a per-segment match missed entirely.
func TestFindsTheHandoffAcrossShortSegments(t *testing.T) {
	s := segs(
		[3]any{1986, 1988, "what kids know you're from and that's"},
		[3]any{1988, 1990, "exactly uh all right we're gonna take a"},
		[3]any{1990, 1992, "break when we come back plenty of headlines"},
		[3]any{1992, 1994, "to get into if you want to skip out on"},
		[3]any{1994, 1995, "these ad messages we give you way to do"},
		[3]any{1995, 1997, "that giant bomb dot com slash join has the"},
		[3]any{1997, 1999, "ad free version of the audio and video"},
		[3]any{1999, 2001, "show there go grab those we will be"},
		[3]any{2001, 2004, "right back"},
		[3]any{2008, 2010, "all right we are back it's game mass"},
	)
	got := FindBreakAnnouncements(s)
	if len(got) != 1 {
		t.Fatalf("expected one span, got %d: %v", len(got), got)
	}
	if got[0].Start != 1988 || got[0].End != 2004 {
		t.Fatalf("expected 1988-2004, got %.0f-%.0f", got[0].Start, got[0].End)
	}
}
