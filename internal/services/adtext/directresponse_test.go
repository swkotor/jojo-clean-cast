package adtext

import "testing"

// The three ad reads from a real Jeff Gerstmann Show episode (VJ7jbNxhF2Y),
// transcribed from the uncut download. SponsorBlock contributors marked them
// 241.7-315.0, 2502.9-2670.8 and 4351.3-4458.0, which is the yardstick here:
// every span must sit INSIDE the human-marked one, so the worst error is a
// second of ad left behind rather than a second of the show removed.
func jeffHims() []Segment {
	return segs(
		[3]any{230, 235, "Like who has the time for that?"},
		[3]any{235, 240, "Not me man. Let's take a break. We'll come back. We'll talk about video games."},
		[3]any{240, 243, "I'm losing my freaking mind"},
		[3]any{243, 248, "About half of men see hair loss by age 50"},
		[3]any{248, 254, "But it can start way earlier and quietly if your hairline's been creeping back"},
		[3]any{254, 262, "Hims can help, access to prescription treatment that stops hair loss with a licensed provider guiding the way"},
		[3]any{262, 270, "Hims is built on the science of regrowth."},
		[3]any{270, 278, "an ingredient shown to stop hair loss and minoxidil to regrow hair in as little as three to six months"},
		[3]any{278, 284, "Combined into a plan personalized to you, getting started is simple"},
		[3]any{284, 290, "answer a few questions online and connect with a licensed provider who determines if treatment is right for you"},
		[3]any{290, 296, "If prescribed it ships right to your door, no pharmacy trips, no insurance necessary"},
		[3]any{296, 301, "Treatment starts at just 35 dollars a month. Hair loss doesn't have to be permanent"},
		[3]any{301, 306, "Ready to regrow your hair? Start your free online visit today at hims.com"},
		[3]any{306, 312, "slash Jeff for personalized care from hair to ED to weight loss and more. That's hims.com slash"},
		[3]any{312, 325, "Jeff. All right. Video games. I have been putting the multiplayer aspects of Call of Duty"},
		[3]any{325, 330, "Modern Warfare through its paces, that was as we talked about a little"},
	)
}

func TestFindsADirectResponseRead(t *testing.T) {
	got := FindDirectResponseAds(jeffHims(), 10357)
	if len(got) != 1 {
		t.Fatalf("expected one span, got %v", Describe(got))
	}
	// inside SponsorBlock's 241.7-315.0, and never into the show at 312+
	if got[0].Start < 235 || got[0].End > 315 {
		t.Fatalf("span %.0f-%.0f is outside the marked ad", got[0].Start, got[0].End)
	}
	if got[0].Seconds() < 60 {
		t.Fatalf("expected most of the 73s read, got %.0fs", got[0].Seconds())
	}
}

// A read whose opening is pure narrative: the first offer marker is half a
// minute after the break cue, which an earlier version measured its drift
// limit from and so produced nothing at all.
func TestFindsAReadWithANarrativeOpening(t *testing.T) {
	s := segs(
		[3]any{2491, 2497, "A fake video game you would make for a 3D Mark benchmark kind of thing."},
		[3]any{2497, 2505, "Let's take one more break. We'll come back. We'll get into the news after this"},
		[3]any{2505, 2512, "Whether it's power outages or long airplane rides or just being out and about you're gonna need a power bank"},
		[3]any{2512, 2518, "That's right, a portable way you can recharge USB devices, your phone"},
		[3]any{2518, 2523, "whatever other stuff, your headphones, all sorts of stuff like that"},
		[3]any{2523, 2528, "Ridge is making it really easy for you"},
		[3]any{2532, 2537, "having a friends and family sale October 8th through November 5th you can buy"},
		[3]any{2537, 2543, "two Ridge power banks and you will get a third one for free"},
		[3]any{2640, 2648, "Free shipping, a 99 day risk free trial and a lifetime warranty."},
		[3]any{2650, 2658, "head to Ridge.com/Jeffgee for the friends and family sale"},
		[3]any{2661, 2663, "After you purchase, they'll ask where you heard about us."},
		[3]any{2663, 2667, "Please support the show and tell them that we sent you."},
		[3]any{2671, 2677, "All right, in the news, Electronic Arts has shut down BioWare."},
	)
	got := FindDirectResponseAds(s, 10357)
	if len(got) != 1 {
		t.Fatalf("expected one span, got %v", Describe(got))
	}
	// must start at the break cue, not 45s into the read, and must not take
	// the news that follows
	if got[0].Start > 2506 || got[0].End > 2671 {
		t.Fatalf("span %.0f-%.0f: expected ~2505-2667", got[0].Start, got[0].End)
	}
}

// Offer language without anywhere to take the offer is just conversation.
func TestIgnoresOfferTalkWithNoDestination(t *testing.T) {
	s := segs(
		[3]any{100, 110, "The Steam sale has it at 50 percent off which is wild for a game this new"},
		[3]any{110, 120, "and I think shipping was free as well when I ordered the physical edition"},
		[3]any{120, 130, "so yeah, I'd say pick it up if you've been waiting on it"},
	)
	if got := FindDirectResponseAds(s, 3000); len(got) != 0 {
		t.Fatalf("expected no cut, got %v", Describe(got))
	}
}

// A read already removed must not be re-detected from its surroundings.
func TestIgnoresAnEpisodeWhoseAdsAreGone(t *testing.T) {
	s := segs(
		[3]any{235, 240, "Not me man. Let's take a break. We'll come back. We'll talk about video games."},
		[3]any{243, 252, "All right. Video games. I have been putting the multiplayer aspects of Call of Duty through its paces"},
		[3]any{252, 262, "that was a staged rollout where they launched the campaign first"},
	)
	if got := FindDirectResponseAds(s, 10010); len(got) != 0 {
		t.Fatalf("expected no cut, got %v", Describe(got))
	}
}
