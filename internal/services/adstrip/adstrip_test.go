package adstrip

import "testing"

// synthFrame builds a valid MPEG-1 Layer III, 128kbps, 44.1kHz frame whose
// payload is filled with `fill`, so frames can be told apart by content.
func synthFrame(fill byte) []byte {
	f := make([]byte, 417)
	f[0] = 0xFF
	f[1] = 0xFB // MPEG-1, Layer III, no CRC
	f[2] = 0x90 // bitrate idx 9 (128k), samplerate idx 0 (44.1k), no padding
	f[3] = 0xC0
	for i := 4; i < len(f); i++ {
		f[i] = fill
	}
	return f
}

func build(fills ...byte) []byte {
	var out []byte
	for _, f := range fills {
		out = append(out, synthFrame(f)...)
	}
	return out
}

func TestParseReadsFrames(t *testing.T) {
	frames := ParseBytes(build(1, 2, 3))
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	if d := Duration(frames); d < 0.078 || d > 0.079 {
		t.Fatalf("duration %.4f, want ~0.0784", d)
	}
}

// The core promise: content frames shared by both downloads survive, and a
// stretch present in only one of them (an inserted ad) is removed.
func TestIntersectRemovesDivergentRun(t *testing.T) {
	// show:  1 2 3 4 5
	// A = show with ads 90,91 in the middle; B = same show, ad 80 there.
	a := ParseBytes(build(1, 2, 90, 91, 3, 4, 5))
	b := ParseBytes(build(1, 2, 80, 3, 4, 5))

	res, err := Intersect([][]Frame{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 5 {
		t.Fatalf("kept %d frames, want 5 (the show)", len(res.Frames))
	}
	for i, want := range []byte{1, 2, 3, 4, 5} {
		if res.Frames[i].Data[4] != want {
			t.Fatalf("frame %d is %d, want %d - content order broken", i, res.Frames[i].Data[4], want)
		}
	}
	if len(res.Gaps) != 1 {
		t.Fatalf("found %d gaps, want 1", len(res.Gaps))
	}
}

// A pre-roll only one variant received must be stripped from the front.
func TestIntersectRemovesPreroll(t *testing.T) {
	a := ParseBytes(build(70, 71, 1, 2, 3))
	b := ParseBytes(build(1, 2, 3))
	res, err := Intersect([][]Frame{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) != 3 || res.Frames[0].Data[4] != 1 {
		t.Fatalf("kept %d frames starting %d, want 3 starting 1", len(res.Frames), res.Frames[0].Data[4])
	}
}

func TestIntersectNeedsTwo(t *testing.T) {
	if _, err := Intersect([][]Frame{ParseBytes(build(1))}); err == nil {
		t.Fatal("want an error with a single variant")
	}
}

// Nothing in common means we are not looking at the same episode; better to
// fail than to emit a file stitched out of unrelated audio.
func TestIntersectRejectsUnrelated(t *testing.T) {
	a := ParseBytes(build(1, 2, 3))
	b := ParseBytes(build(50, 51, 52))
	if _, err := Intersect([][]Frame{a, b}); err == nil {
		t.Fatal("want an error when the downloads share no audio")
	}
}
