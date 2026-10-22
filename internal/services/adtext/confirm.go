package adtext

// Confirming SponsorBlock segments against the transcript.
//
// yt-dlp refuses a video's SponsorBlock segments outright when the durations
// disagree ("Some SponsorBlock segments are from a video of different
// duration"), because cutting with misaligned timestamps would butcher the
// show. That is the right call for yt-dlp, which cannot tell whether the
// segments are stale or merely mis-measured — but it means a Kinda Funny Games
// Daily episode kept all 173s of its ads (a Digital Eclipse spot at 0:00 and a
// 152s ProtonVPN/Alienware block at 41:35) while the database recorded 173s as
// removed.
//
// We can do better than refuse, because we have the transcript. Rather than
// trusting the timestamps, we check what is actually being said inside each
// marked window: if it reads as advertising copy, the segment is where it
// claims to be and is safe to cut. Nothing is cut on the strength of the
// timestamps alone.

import (
	"regexp"
)

// Advertising vocabulary, weighted. These are broader than the host-read rules
// in adtext.go on purpose: here a human (the SponsorBlock submitter) has
// already said "there is an ad here", so the job is confirmation, not
// discovery, and recall matters more than precision.
var adSignals = []struct {
	weight int
	re     *regexp.Regexp
}{
	{6, regexp.MustCompile(`(?i)\b(promo|discount|offer) code\b|\buse code\b`)},
	{5, regexp.MustCompile(`(?i)\bsponsor(ed|s|ship)?\b|\bbrought to you by\b|\ba word from our\b`)},
	{4, regexp.MustCompile(`(?i)\b(ad|advert)[ -]?free\b`)},
	{4, regexp.MustCompile(`(?i)\bfree trial\b|\bfirst month\b|\b\d{1,2}( ?%| percent) off\b|\bmoney[ -]back\b`)},
	{3, regexp.MustCompile(`(?i)\b(patreon|membership|premium member|members only)\b`)},
	{3, regexp.MustCompile(`(?i)\b[a-z0-9-]+ ?(\.|dot )(com|co\.uk|net|org|io|tv) ?(/|slash) ?[a-z]`)},
	{3, regexp.MustCompile(`(?i)\bper month\b|\ba month\b|\$\d`)},
	{4, regexp.MustCompile(`(?i)\b(available|out) now (on|at|in)\b|\bavailable on\b.{0,24}\b(steam|console|pc|xbox|playstation)\b`)},
	{2, regexp.MustCompile(`(?i)\b(on|for) (pc|steam|consoles?|xbox|playstation|switch)\b|\ball major consoles\b|\bphysical edition\b`)},
	{2, regexp.MustCompile(`(?i)\bbe sure to\b|\bjump in and play\b|\bnewest releases?\b`)},
	{2, regexp.MustCompile(`(?i)\b(head|go|jump|pop) (on )?(over )?to\b|\bvisit\b|\bsign up\b|\bcheck out\b`)},
	{2, regexp.MustCompile(`(?i)\b(terms (and conditions|apply)|limited time|exclusions apply|restrictions apply)\b`)},
	{2, regexp.MustCompile(`(?i)\b[a-z0-9-]{3,}\.(com|co\.uk|net|org|io|tv)\b|\bdot com\b`)},
}

// adScore totals the advertising signals in a stretch of text. Each pattern
// counts once however often it recurs, so a single repeated phrase cannot
// carry a window on its own.
func adScore(text string) int {
	n := 0
	for _, s := range adSignals {
		if s.re.MatchString(text) {
			n += s.weight
		}
	}
	return n
}

// confirmScore is the score a marked window must reach to be believed. Two
// independent mid-weight signals, or one strong one plus a weak one.
const confirmScore = 6

// driftSearch is how far either side we look for the segments' true position,
// and the step. A duration disagreement usually means every segment is shifted
// by the same amount, so one global offset is searched rather than moving each
// window independently — which would let an offset be invented per segment
// until something scored.
const (
	driftSearch = 30.0
	driftStep   = 5.0
)

func textIn(segs []Segment, a, b float64) string {
	out := ""
	for _, s := range segs {
		if s.End > a && s.Start < b {
			out += " " + s.Text
		}
	}
	return out
}

// snap widens a window to whole transcript segments, so a cut never lands
// mid-sentence inside the show.
func snap(segs []Segment, a, b float64) (float64, float64) {
	lo, hi, found := 0.0, 0.0, false
	for _, s := range segs {
		if s.End > a && s.Start < b {
			if !found {
				lo, hi, found = s.Start, s.End, true
				continue
			}
			if s.Start < lo {
				lo = s.Start
			}
			if s.End > hi {
				hi = s.End
			}
		}
	}
	if !found {
		return a, b
	}
	return lo, hi
}

// ConfirmSponsorSpans returns the subset of hints whose transcript reads as
// advertising, shifted by the single offset that fits them best.
//
// It returns nil rather than a partial guess when nothing scores: leaving the
// ads in is recoverable, cutting the show is not.
func ConfirmSponsorSpans(segs []Segment, hints []Span, total float64) []Span {
	if len(segs) == 0 || len(hints) == 0 {
		return nil
	}
	offset, score := bestOffset(segs, hints)
	if score <= 0 {
		return nil
	}

	var out []Span
	for _, h := range hints {
		a, b := h.Start+offset, h.End+offset
		if a < 0 {
			a = 0
		}
		if b > total {
			b = total
		}
		if b-a < 3 {
			continue
		}
		txt := textIn(segs, a, b)
		if adScore(txt) < confirmScore {
			continue // marked, but nothing ad-like is being said there
		}
		a, b = snap(segs, a, b)
		reason := txt
		if len(reason) > 70 {
			reason = reason[:70]
		}
		out = append(out, Span{Start: a, End: b, Reason: reason})
	}
	return mergeSpans(out)
}

// offsetMargin is how much better a shifted alignment must score before it is
// preferred over one closer to zero.
//
// Without it the search always drifted: sliding the window a few seconds
// usually swallows MORE of the surrounding ad read, so the score kept rising
// and the chosen span crept past the segment's real end into the show.
const offsetMargin = 3

// bestOffset finds the single shift that fits the hints.
//
// The timestamps are presumed right: if every hint already confirms where it
// says it is, no shift is considered at all. Searching in that case made
// things worse — sliding a window a few seconds usually swallows more of the
// surrounding ad read, so the score kept rising and the span crept past the
// segment's real end into the show.
func bestOffset(segs []Segment, hints []Span) (float64, int) {
	confirmed := func(off float64) (int, int) {
		sum, n := 0, 0
		for _, h := range hints {
			if sc := adScore(textIn(segs, h.Start+off, h.End+off)); sc >= confirmScore {
				sum += sc
				n++
			}
		}
		return sum, n
	}
	if sum, n := confirmed(0); n == len(hints) {
		return 0, sum
	}
	bestOff, best := 0.0, 0
	if sum, _ := confirmed(0); sum > 0 {
		best = sum
	}
	// A shift is only believed when it fits EVERY segment. A duration
	// disagreement moves all of them by the same amount, so a candidate that
	// suits one window and not the rest is a coincidence, not an alignment —
	// and acting on one would cut a stretch of show that happens to read a
	// little like a promo. Checked against a Kinda Funny Gamescast episode
	// whose ads were already gone: a -40s shift matched 1 of 3 segments and
	// would have taken 158s of the show with it.
	for d := driftStep; d <= driftSearch; d += driftStep {
		for _, off := range []float64{d, -d} {
			sc, n := confirmed(off)
			if n == len(hints) && sc > best+offsetMargin {
				bestOff, best = off, sc
			}
		}
	}
	return bestOff, best
}

// Offset reports the alignment shift chosen for a hint set, for logging.
func Offset(segs []Segment, hints []Span) float64 {
	off, _ := bestOffset(segs, hints)
	return off
}
