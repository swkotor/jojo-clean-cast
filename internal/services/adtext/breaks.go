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

import "regexp"

// entering a break
var breakOpenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\blet'?s (take|have) a (quick |short |little )?break\b`),
	regexp.MustCompile(`(?i)\bwe'?(ll|re going to) (take|have) a (quick |short )?break\b`),
	regexp.MustCompile(`(?i)\bskip (out on |past |over )?(these|the|those)? ?(ad|ads|ad messages|ad breaks|advertisements)\b`),
	regexp.MustCompile(`(?i)\b(time|first) for (a|these|some) (quick )?(ad|ads|break|messages)\b`),
	regexp.MustCompile(`(?i)\bback after (this|these|the break|a break)\b`),
}

// coming out of it
var breakClosePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(be|we'?ll be|right) back (after|in a|right)?\b`),
	regexp.MustCompile(`(?i)\bwe (will|'ll) be right back\b`),
	regexp.MustCompile(`(?i)\b(and )?we'?re back\b`),
	regexp.MustCompile(`(?i)\bwelcome back\b`),
}

// corroboration that this really is an ad break and not a coffee break
var breakCorroborate = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ad|ads|advert|adverts|ad messages|advertisements)\b`),
	regexp.MustCompile(`(?i)\b(ad|advert)[ -]?free\b`),
	regexp.MustCompile(`(?i)\bsponsor`),
}

// how far past the opening cue the closing cue may appear
const breakWindow = 90.0

// maximum length of a single announcement
const breakMaxSpan = 75.0

// FindBreakAnnouncements returns spans covering scripted ad-break hand-offs.
//
// A span is only produced when an opening cue is followed, within
// breakWindow seconds, by a closing cue, and the text in between mentions ads
// or sponsorship. All three are required: the opening cue alone fires on
// ordinary speech, and without the ad mention a genuine "let's take a break,
// back in a sec" would be cut.
func FindBreakAnnouncements(segs []Segment) []Span {
	var out []Span
	for i, s := range segs {
		if !matches(breakOpenPatterns, s.Text) {
			continue
		}
		if len(out) > 0 && s.Start < out[len(out)-1].End {
			continue // already inside a span we produced
		}
		adMention := matches(breakCorroborate, s.Text)
		end := -1.0
		for j := i; j < len(segs) && segs[j].Start-s.Start <= breakWindow; j++ {
			if matches(breakCorroborate, segs[j].Text) {
				adMention = true
			}
			if j > i && matches(breakClosePatterns, segs[j].Text) {
				end = segs[j].End
				break
			}
		}
		if end < 0 || !adMention {
			continue
		}
		if end-s.Start > breakMaxSpan {
			continue // too long to be just the announcement; leave it alone
		}
		out = append(out, Span{Start: s.Start, End: end})
	}
	return out
}
