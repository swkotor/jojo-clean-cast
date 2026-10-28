package adtext

import "testing"

// A 75s Merchantech spot from a real Game Mess Mornings episode, performed as
// a bit between the hosts. It carries no sponsor vocabulary at all, so every
// other rule missed it; what gives it away is the name, six times in fifty
// seconds and nowhere else in the episode, next to its own address.
func TestFindsAnAdPerformedAsABit(t *testing.T) {
	s := segs(
		[3]any{1464, 1469, "and more broadly what am I gonna do? I know plenty about starting a business but I don't know"},
		[3]any{1469, 1474, "anything about spinning up a store. Jeff Grub, don't worry, my best friend"},
		[3]any{1474, 1478, "Merchant Tech can help you do that. I thought I was your best friend Mike Manani."},
		[3]any{1478, 1484, "Not anymore. Merchant Tech is here to help support independent sellers like you. They can do it"},
		[3]any{1484, 1487, "at all in one easy to use platform."},
		[3]any{1487, 1489, "But what if I need someone to help hold my hand"},
		[3]any{1492, 1494, "Will you help hold my hand through this?"},
		[3]any{1497, 1502, "But Merchantech will, they have white glove customer service."},
		[3]any{1505, 1508, "I hope Merchantech will be my new best friend too."},
		[3]any{1508, 1511, "I needed a way to figure out how to make a merch store,"},
		[3]any{1518, 1521, "my BFF merchant tech is here for you."},
		[3]any{1524, 1530, "Hit up our friends over at merchant.tech slash giant bomb."},
		[3]any{1532, 1540, "Right, so anyway, back to the Respawn news and what it means."},
	)
	got := FindBrandReads(s, 3307)
	if len(got) != 1 {
		t.Fatalf("expected the Merchantech read to be found, got %v", Describe(got))
	}
	if got[0].Start > 1474 || got[0].End < 1524 {
		t.Fatalf("span %.0f-%.0f misses part of the read", got[0].Start, got[0].End)
	}
	// and it must not run on into the news that follows
	if got[0].End > 1532 {
		t.Fatalf("span ends at %.0f, into the show", got[0].End)
	}
}

// The same spot a week later, transcribed with the brand written consistently
// as two words so it yields SIX mentions rather than four. The cluster only
// grew forward, so the first two fell outside it and the concentration test
// rejected the read at 4/6. The four-mention case above could not catch this.
func TestFindsABrandReadWithMoreMentionsThanTheMinimum(t *testing.T) {
	s := segs(
		[3]any{6424, 6432, "Gang, we're going to take a quick break and we will be back with the news right after this."},
		[3]any{6433, 6439, "Gosh, what am I going to do? I know plenty about starting a business, but I don't know anything about spinning up a store."},
		[3]any{6439, 6442, "Jeff Grubb, don't worry."},
		[3]any{6442, 6445, "My best friend, Merchant Tech, can help you do that."},
		[3]any{6445, 6446, "I thought I was your best friend, Mike Manani."},
		[3]any{6446, 6447, "Not anymore."},
		[3]any{6447, 6452, "Merchant Tech is here to help support independent sellers like you."},
		[3]any{6452, 6455, "They can do it all in one easy to use platform."},
		[3]any{6462, 6467, "No, I don't want to touch your disgusting hand grub, but Merchant Tech will."},
		[3]any{6474, 6477, "I hope Merchant Tech will be my new best friend too."},
		[3]any{6487, 6490, "BFF Merchant Tech is here for you."},
		[3]any{6490, 6496, "Easy to use tools and unmatched customer service, hit up our friends over at merchant.tech"},
		[3]any{6496, 6500, "Slash giant bomb."},
		[3]any{6526, 6532, "So, that's it for now."},
	)
	got := FindBrandReads(s, 11018)
	if len(got) != 1 {
		t.Fatalf("expected the read to be found, got %v", Describe(got))
	}
	// the whole read, including the hand-off that introduces it and the
	// "slash giant bomb" that finishes the address
	if got[0].Start > 6424 || got[0].End < 6500 {
		t.Fatalf("span %.0f-%.0f misses part of the read", got[0].Start, got[0].End)
	}
	if got[0].End > 6526 {
		t.Fatalf("span runs into the news at %.0f", got[0].End)
	}
}
