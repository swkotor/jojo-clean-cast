package adtext

import "testing"

// Each of these is a real case Joel found by listening, with the spans the app
// cut at the time. The audit must flag every one.
func TestAuditFlagsTheAdsWeMissed(t *testing.T) {
	cases := []struct {
		name string
		segs []Segment
		cut  []Span
	}{
		{"merchantech, nothing cut", segs(
			[3]any{6487, 6490, "BFF Merchant Tech is here for you."},
			[3]any{6490, 6496, "unmatched customer service, hit up our friends over at merchant.tech"},
			[3]any{6496, 6500, "Slash giant bomb."},
		), nil},
		{"hims, nothing cut", segs(
			[3]any{301, 306, "Ready to regrow your hair? Start your free online visit today at hims.com"},
			[3]any{306, 312, "slash Jeff for personalized care. That's hims.com slash Jeff."},
		), nil},
		{"perfect jean promo code", segs(
			[3]any{4449, 4458, "Use the code JeffG15, JeffG15 for 15% off."},
		), nil},
		{"kinda funny digital eclipse", segs(
			[3]any{0, 9, "Be sure to jump in and play with Woody and Buzz in the newest releases from Digital Eclipse."},
			[3]any{2545, 2552, "This episode's brought to you by ProtonVPN."},
		), nil},
		// The cut covered the address but began 45s into the read, so the
		// listener still got its opening. There is no address left outside the
		// span to find — this is caught by the shape of the cut instead.
		{"ridge, cut landed 45s late", segs(
			[3]any{2497, 2505, "Let's take one more break. We'll come back. We'll get into the news after this"},
			[3]any{2505, 2512, "Whether it's power outages or long airplane rides you're gonna need a power bank"},
			[3]any{2650, 2658, "head to Ridge.com/Jeffgee for the friends and family sale"},
		), []Span{{Start: 2550, End: 2668}}},
	}
	for _, c := range cases {
		got := AuditLeftovers(c.segs, c.cut, nil)
		if len(got) == 0 {
			t.Errorf("%s: audit found nothing", c.name)
		} else {
			t.Logf("%s -> %s", c.name, DescribeLeftovers(got))
		}
	}
}

// A show's own links are a choice, not a miss, and flagging them would bury
// the real findings.
func TestAuditIgnoresTheShowsOwnPromotion(t *testing.T) {
	s := segs(
		[3]any{186, 192, "Go to giantbomb.com/join become a premium member, support independent games media."},
		[3]any{2459, 2463, "go pick up a membership, patreon.com slash kind of funny"},
		[3]any{2463, 2467, "YouTube.com slash kind of funny games, Apple Spotify, wherever you"},
		[3]any{6954, 6958, "get in there at patreon.com slash easy allies."},
	)
	if got := AuditLeftovers(s, nil, []string{"giantbomb"}); len(got) != 0 {
		t.Fatalf("expected nothing, got %s", DescribeLeftovers(got))
	}
}

// An ad inside the span we cut is not a leftover.
func TestAuditIgnoresWhatWasCut(t *testing.T) {
	s := segs(
		[3]any{301, 306, "Start your free online visit today at hims.com"},
		[3]any{306, 312, "slash Jeff. That's hims.com slash Jeff."},
	)
	if got := AuditLeftovers(s, []Span{{Start: 240, End: 312}}, nil); len(got) != 0 {
		t.Fatalf("expected nothing, got %s", DescribeLeftovers(got))
	}
}

// Ordinary talk about games and websites must not flag.
func TestAuditIgnoresOrdinarySpeech(t *testing.T) {
	s := segs(
		[3]any{100, 110, "I read it on ign.com earlier, the review went up this morning"},
		[3]any{110, 120, "and the Steam sale has it at 50 percent off which is wild"},
		[3]any{120, 130, "you can go to the vote here button on giant bomb.com and add your game"},
		[3]any{130, 140, "did you see that eurogamer.net piece about the layoffs"},
	)
	if got := AuditLeftovers(s, nil, nil); len(got) != 0 {
		t.Fatalf("expected nothing, got %s", DescribeLeftovers(got))
	}
}
