package adtext

// Detecting classic direct-response ad reads.
//
// The sponsor rules in adtext.go look for podcast self-promo — patreon,
// membership, ad-free, "support the show". The brand rule in brand.go looks for
// a name hammered next to its own address. Neither describes the ads on a show
// like The Jeff Gerstmann Show, which are ordinary direct-response radio copy
// read by the host, with no sponsor noun anywhere in them:
//
//	"About half of men see hair loss by age 50 ... treatment can include
//	 finasteride ... ships right to your door, no pharmacy trips, no insurance
//	 necessary ... treatment starts at just 35 dollars a month ... start your
//	 free online visit today at hims.com/jef"
//
//	"15% off your first order plus free shipping at theperfectgene.nyc ... use
//	 the code JeffG15"
//
// Measured on one episode: of 348s of real ads across three slots, the existing
// rules found 118s, all of it from the single slot whose advertiser was named
// often enough for the brand rule. The other two reads were invisible.
//
// What this copy always has is an OFFER and somewhere to take it. Requiring
// both — two distinct offer markers and either a vanity path or a promo code —
// is what keeps it off ordinary speech: a games podcast says "free shipping"
// or "fifteen percent off" occasionally, but not alongside "hims.com/jef".

import (
	"regexp"
	"sort"
)

// The language of an offer. Each counts once per window however often it
// recurs, so one repeated phrase cannot carry a window alone.
var drMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bfree shipping\b`),
	regexp.MustCompile(`(?i)\b(risk[ -]free|money[ -]back|satisfaction) (trial|guarantee)\b|\b\d+ day risk\b`),
	regexp.MustCompile(`(?i)\blifetime warranty\b`),
	regexp.MustCompile(`(?i)\b\d{1,2} ?(%|percent) off\b`),
	regexp.MustCompile(`(?i)\b(your|the) first (order|purchase|month|box)\b`),
	regexp.MustCompile(`(?i)\b(starts|starting) at (just |only )?[$£€]?\d+`),
	regexp.MustCompile(`(?i)([$£€]\s?\d+|\b\d+ (pounds|dollars|euros|bucks)) ?(a|per) month\b`),
	regexp.MustCompile(`(?i)\bbuy (two|three|2|3|one)\b.{0,40}\b(get|and get)\b.{0,30}\b(free|third|second)\b`),
	regexp.MustCompile(`(?i)\bships? (right )?to your door\b|\bno (pharmacy trips|insurance necessary|commitment)\b`),
	regexp.MustCompile(`(?i)\b(licensed|medical) (provider|team)\b|\bif prescribed\b|\bfree online visit\b`),
	regexp.MustCompile(`(?i)\b(friends and family|black friday|holiday|anniversary) sale\b`),
	regexp.MustCompile(`(?i)\bwhere you heard about\b|\btell them (that )?we sent you\b|\bsupport the show and\b`),
	regexp.MustCompile(`(?i)\bavailable (now )?at\b.{0,30}\b(best buy|amazon|target|walmart|costco)\b`),
	regexp.MustCompile(`(?i)\b(terms|exclusions|restrictions) (and conditions )?apply\b|\bwhile supplies last\b`),
}

// Somewhere to take the offer. A vanity path or a promo code is what makes this
// advertising rather than a passing mention of a website.
var drDestination = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z0-9-]{3,} ?(?:\.|dot )(?:com|co\.uk|net|org|io|tv|tech|co|shop|store|app|nyc|us|xyz|gg|fm) ?(?:/|slash) ?[a-z0-9]`),
	regexp.MustCompile(`(?i)\buse (the )?(promo |discount )?code\b|\b(promo|discount|offer) code\b`),
}

const (
	drWindow     = 20.0  // how far apart the markers may sit
	drMinMarkers = 2     // distinct markers needed
	drDrift      = 20.0  // how long a read may go without offer language
	drMinSpan    = 30.0  // a read is never shorter than this
	drMaxSpan    = 260.0 // nor longer
)

// distinctMarkers counts how many different offer markers appear in a text.
func distinctMarkers(text string) int {
	n := 0
	for _, re := range drMarkers {
		if re.MatchString(text) {
			n++
		}
	}
	return n
}

// FindDirectResponseAds returns spans of host-read direct-response copy.
//
// The edges come from the read's STRUCTURE, not from where the offer markers
// happen to fall. Markers cluster at the end of a read — the price, the
// shipping, the URL — while the opening is narrative ("About half of men see
// hair loss by age 50", "Whether it's power outages or long airplane rides").
// Growing a span from the markers outwards put all three of this episode's
// spans 45-65s late at the front and 20s long at the back, which would have
// left the opening of each ad in and cut the news that followed it.
//
// Anchoring instead on the break announcement before the read and the last
// segment that itself carries a marker or the destination reproduces
// SponsorBlock's own boundaries for this episode to within a few seconds:
// 241-312 against 241.7-315.0, 2505-2667 against 2502.9-2670.8, and 4353-4458
// against 4351.3-4458.0.
func FindDirectResponseAds(segs []Segment, total float64) []Span {
	if len(segs) < 3 {
		return nil
	}
	// own[i]: this segment itself carries offer language or the destination.
	// Membership is judged on the segment's own text — deciding it from the
	// surrounding window is what made the spans overshoot, because a segment
	// 20s past the ad still sees the ad's markers.
	own := make([]bool, len(segs))
	for i := range segs {
		own[i] = distinctMarkers(segs[i].Text) > 0 || matches(drDestination, segs[i].Text)
	}
	// seed[i]: an offer is being made around here.
	seed := make([]bool, len(segs))
	for i := range segs {
		seed[i] = distinctMarkers(window(segs, i, drWindow)) >= drMinMarkers
	}

	var out []Span
	for i := 0; i < len(segs); i++ {
		if !seed[i] || (len(out) > 0 && segs[i].Start < out[len(out)-1].End) {
			continue
		}
		// Front edge: the hand-off into the break. Everything after
		// "we'll come back ... after this" and before the show resumes is the
		// ad. Without a cue we fall back to the first segment carrying offer
		// language, which can begin mid-read.
		startAt := -1
		for j := i; j >= 0 && segs[i].Start-segs[j].End <= 200; j-- {
			if matches(breakOpenPatterns, segs[j].Text) {
				startAt = j
				break
			}
		}
		a := segs[i].Start
		if startAt >= 0 {
			a = segs[startAt].End
		} else {
			for j := i; j >= 0 && segs[i].Start-segs[j].Start <= 200; j-- {
				if own[j] {
					a = segs[j].Start
				}
			}
		}

		// A later seed inside a read we have already emitted describes the same
		// ad. Letting it through produced a second, degenerate candidate that
		// began at the same break cue but with an empty body, which the
		// address logic below then inflated past the end of the ad — and the
		// union of the two spans ran 19s into the show.
		if len(out) > 0 && a <= out[len(out)-1].End {
			continue
		}

		// Back edge: the last segment within reach that is itself part of the
		// read. Stop at a silence, and never run past the cap.
		b, last := a, i
		markers := map[int]bool{}
		// Walk forward only as far as the read reaches. The stopping condition
		// is how long it has been since a segment that is itself part of the
		// read: without it the loop scanned the whole drMaxSpan window and
		// latched onto an unrelated marker minutes later, so a span that
		// should have ended on "that's hims.com slash Jeff" ran 19s into the
		// Call of Duty discussion that followed.
		// The drift limit applies only AFTER the read's offer language has
		// started. Measuring it from the break cue instead broke the two reads
		// whose openings are narrative — "Whether it's power outages or long
		// airplane rides", "I recently got a great opportunity to put the
		// perfect jean to the test" — where the first marker is half a minute
		// in, and the span collapsed to nothing.
		lastOwn, seenOwn := a, false
		for j := i; j < len(segs) && segs[j].End-a <= drMaxSpan; j++ {
			if seenOwn && segs[j].Start-lastOwn > drDrift {
				break
			}
			if j > 0 && !adjacent(segs, j-1, j) {
				break
			}
			if own[j] {
				b, last, lastOwn, seenOwn = segs[j].End, j, segs[j].End, true
				for k, re := range drMarkers {
					if re.MatchString(segs[j].Text) {
						markers[k] = true
					}
				}
			}
		}
		// Finish the address. A read signs off "...today at hims.com" / "slash
		// Jeff", so the destination straddles the last two segments and a span
		// that stopped at the first of them failed its own destination check
		// and was discarded — the Hims ad was lost exactly this way.
		for n := 0; n < 2 && last+1 < len(segs) && b > a; n++ {
			nx := last + 1
			if !adjacent(segs, last, nx) {
				break
			}
			completes := !matches(drDestination, textIn(segs, a, b)) &&
				matches(drDestination, textIn(segs, a, segs[nx].End))
			if continuesAddress(segs, a, b, nx) || matches(drDestination, segs[nx].Text) || completes {
				b, last = segs[nx].End, nx
				continue
			}
			break
		}

		// Also count markers from the narrative opening we pulled in.
		for k, re := range drMarkers {
			if re.MatchString(textIn(segs, a, b)) {
				markers[k] = true
			}
		}
		if len(markers) < drMinMarkers || !matches(drDestination, textIn(segs, a, b)) {
			i = last
			continue
		}
		if d := b - a; d >= drMinSpan && d <= drMaxSpan {
			reason := textIn(segs, a, a+8)
			if len(reason) > 60 {
				reason = reason[:60]
			}
			out = append(out, Span{Start: a, End: b, Reason: "direct response:" + reason})
		}
		i = last
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return mergeSpans(out)
}

// attributionWindow is how far an advertiser's address may sit from the
// sponsor attribution that introduces it.
const attributionWindow = 150.0

// FindAttributedAds returns reads introduced as a sponsor and signed off with
// a third-party address.
//
// Brand-awareness copy has no offer in it at all — no price, no shipping, no
// code — so the direct-response rule cannot see it, and the advertiser may be
// named only two or three times, below the brand rule's threshold:
//
//	"You know what, this sounds like a great time to hear word from our
//	 sponsor. ... Gaming today isn't one thing ... That's why Alienware
//	 systems are built with the performance to handle all of it ... Be more
//	 alien at alienware.com slash game your way."
//
// Two pieces of evidence, each weak alone and conclusive together: the hosts
// said a sponsor was coming, and an address that is not theirs follows. This
// pairing is what the leftover audit uses to report a miss, so detecting it
// closes the loop — the audit found these two episodes.
func FindAttributedAds(segs []Segment, total float64, selfHosts []string) []Span {
	if len(segs) < 3 {
		return nil
	}
	attribution := regexp.MustCompile(`(?i)\bword from our sponsors?\b|\bbrought to you by\b|\b(our|this episode's|today's) sponsor\b|\bthanks to our sponsor`)

	var out []Span
	for i := range segs {
		if !attribution.MatchString(segs[i].Text) {
			continue
		}
		if len(out) > 0 && segs[i].Start < out[len(out)-1].End {
			continue
		}
		// Find the advertiser's sign-off after it.
		end, found := 0.0, false
		for j := i; j < len(segs) && segs[j].Start-segs[i].End <= attributionWindow; j++ {
			pair := segs[j].Text
			if j+1 < len(segs) && adjacent(segs, j, j+1) {
				pair += " " + segs[j+1].Text
			}
			if matches(drDestination, pair) && hasThirdPartyHost(pair, selfHosts) {
				end, found = segs[j].End, true
			}
		}
		if !found {
			continue
		}
		a, b := snap(segs, segs[i].Start, end)
		a, b = widenRead(segs, a, b)
		if d := b - a; d >= drMinSpan && d <= drMaxSpan {
			reason := textIn(segs, a, a+8)
			if len(reason) > 60 {
				reason = reason[:60]
			}
			out = append(out, Span{Start: a, End: b, Reason: "attributed:" + reason})
		}
	}
	return mergeSpans(out)
}
