package adtext

// Detecting the "we'll be right back" announcement around an ad break.
//
// Giant Bomb's shows do not read sponsor copy the way VGC does, so the
// sponsor-plus-call-to-action scoring in adtext.go finds almost nothing in
// them. What they do have is a scripted hand-off into the break:
//
//	"Before that though, let's take a break. When we come back, we've got
//	 plenty of headlines to get into. If you want to skip out on these ad
//	 messages, you can do so go to giantbomb.com/join and get the ad-free
//	 version of the audio and video show there. We will be right back."
//
// The inserted ad itself is already gone by this point — the differential pass
// removes it — so what is left is twenty seconds of announcement for an ad that
// is no longer there, which is exactly the "random bit at the ad seam" problem.
//
// This is kept separate from the sponsor scoring on purpose. It is a narrow,
// structural pattern with a required opening AND closing cue, so it can be
// confident where the generic scoring cannot. It deliberately does NOT try to
// remove the membership plug woven through these shows' opening minutes: there
// the pitch is interleaved with real conversation sentence by sentence, and
// anything that cut it would take the show with it.
//
// Matching is done over the whole transcript as one string rather than segment
// by segment. Whisper's segmentation varies a lot between episodes — two
// recordings of the same show came back with 596 and 1578 segments — so the
// same sentence is one segment in one episode and split across three in
// another, and a per-segment match found the hand-off in the first and missed
// it in the second.

import (
	"regexp"
	"sort"
	"strings"
)

// entering a break
var breakOpenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\blet'?s (take|have) a (quick |short |little )?break\b`),
	regexp.MustCompile(`(?i)\bwe'?(ll|re| are|'re gonna|'re going to|ll be) ?(gonna |going to )?(take|have) a (quick |short )?break\b`),
	regexp.MustCompile(`(?i)\bskip (out on|past|over|out) (these|the|those) ?(ad|ads|ad messages|ad breaks|advertisements)\b`),
	regexp.MustCompile(`(?i)\b(time|first) for (a|these|some) (quick )?(ad|ads|break|messages)\b`),
	regexp.MustCompile(`(?i)\bback after (this|these|the break|a break)\b`),
}

// coming out of it
var breakClosePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bright back\b`),
	regexp.MustCompile(`(?i)\bbe back (after|in a|shortly)\b`),
	regexp.MustCompile(`(?i)\bwe'?re back\b`),
	regexp.MustCompile(`(?i)\bwelcome back\b`),
}

// corroboration that this really is an ad break and not a coffee break
var breakCorroborate = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ad|ads|advert|adverts|advertisements) (message|messages|break|breaks)?`),
	regexp.MustCompile(`(?i)\b(ad|advert)[ -]?free\b`),
	regexp.MustCompile(`(?i)\bsponsor`),
}

// how far past the opening cue the closing cue may appear
const breakWindow = 90.0

// maximum length of a single announcement
const breakMaxSpan = 75.0

// flat is the transcript as one string, with a map back to segment indices.
type flat struct {
	text   string
	starts []int // byte offset in text where each segment begins
}

func flatten(segs []Segment) flat {
	var b strings.Builder
	starts := make([]int, len(segs))
	for i, s := range segs {
		starts[i] = b.Len()
		b.WriteString(strings.TrimSpace(s.Text))
		b.WriteByte(' ')
	}
	return flat{text: b.String(), starts: starts}
}

// segAt maps a byte offset back to the segment it fell in.
func (f flat) segAt(off int) int {
	i := sort.SearchInts(f.starts, off+1) - 1
	if i < 0 {
		return 0
	}
	return i
}

// hits returns the segment indices where any of the patterns match.
func (f flat) hits(pats []*regexp.Regexp) []int {
	seen := map[int]bool{}
	var out []int
	for _, p := range pats {
		for _, m := range p.FindAllStringIndex(f.text, -1) {
			if i := f.segAt(m[0]); !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	sort.Ints(out)
	return out
}

// FindBreakAnnouncements returns spans covering scripted ad-break hand-offs.
//
// A span is only produced when an opening cue is followed, within breakWindow
// seconds, by a closing cue, and the text in between mentions ads or
// sponsorship. All three are required: the opening cue alone fires on ordinary
// speech, and without the ad mention a genuine "let's take a break, back in a
// sec" would be cut.
func FindBreakAnnouncements(segs []Segment) []Span {
	if len(segs) == 0 {
		return nil
	}
	f := flatten(segs)
	closes := map[int]bool{}
	for _, i := range f.hits(breakClosePatterns) {
		closes[i] = true
	}
	adMention := map[int]bool{}
	for _, i := range f.hits(breakCorroborate) {
		adMention[i] = true
	}

	var out []Span
	for _, i := range f.hits(breakOpenPatterns) {
		if len(out) > 0 && segs[i].Start < out[len(out)-1].End {
			continue // already inside a span we produced
		}
		sawAd := adMention[i]
		end := -1.0
		for j := i; j < len(segs) && segs[j].Start-segs[i].Start <= breakWindow; j++ {
			if adMention[j] {
				sawAd = true
			}
			// the closing cue must come after the opening one, and we keep
			// scanning past it while the next segment is still part of the
			// announcement (short segments often split "...we'll be" / "right
			// back"), stopping at the last consecutive close hit
			if j > i && closes[j] {
				end = segs[j].End
				for j+1 < len(segs) && closes[j+1] {
					j++
					end = segs[j].End
				}
				break
			}
		}
		if end < 0 || !sawAd || end-segs[i].Start > breakMaxSpan {
			continue
		}
		reason := strings.TrimSpace(segs[i].Text)
		if len(reason) > 70 {
			reason = reason[:70]
		}
		out = append(out, Span{Start: segs[i].Start, End: end, Reason: reason})
	}
	return out
}
