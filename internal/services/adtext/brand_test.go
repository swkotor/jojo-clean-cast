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
