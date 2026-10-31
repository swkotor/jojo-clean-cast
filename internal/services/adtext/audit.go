package adtext

// Checking our own work: what ads are still in the episode after cutting?
//
// Five rounds of "this show still has ads" were each found by Joel listening,
// not by the app. In every case the evidence was sitting in a transcript the
// app had already made and thrown away. The detectors answer "what can I
// find"; this answers the different and more useful question "is there
// anything obviously left", and says so in the event log.
//
// It runs on the transcript MINUS the spans just cut, so it needs no second
// transcription, and it only reports evidence that is hard to argue with: an
// advertiser's vanity address or a promo code, or sponsor-attribution wording.
// A show's own links are excluded, because a Patreon plug left in is a choice,
// not a miss.

import (
	"fmt"
	"regexp"
	"strings"
)

// Hosts a podcast uses to promote itself. A link to one of these is not a
// third-party ad, and flagging them would bury the real findings.
var selfPromoHosts = []string{
	"patreon", "youtube", "youtu", "twitch", "discord", "ko-fi", "kofi",
	"bandcamp", "substack", "apple", "spotify", "podlink", "pod", "linktr",
	"instagram", "twitter", "bsky", "mastodon", "tiktok", "facebook",
	"paypal", "merchbar", "teepublic", "fourthwall", "memberful",
}

// Evidence of a third-party ad, in rough order of how hard it is to explain
// away.
var auditPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"vanity address", regexp.MustCompile(`(?i)\b([a-z0-9-]{3,}) ?(?:\.|dot )(?:com|co\.uk|net|org|io|tv|tech|co|shop|store|app|nyc|us|xyz|gg|fm) ?(?:/|slash) ?[a-z0-9]`)},
	{"promo code", regexp.MustCompile(`(?i)\buse (the )?(promo |discount )?code\b|\b(promo|discount|offer) code\b`)},
	{"sponsor attribution", regexp.MustCompile(`(?i)\bbrought to you by\b|\b(our|this episode's|today's) sponsor\b|\ba word from our sponsors?\b|\bthanks to our sponsor`)},
}

// Leftover is one piece of ad evidence still in the episode.
type Leftover struct {
	At   float64
	What string
	Text string
}

// hostOf pulls the domain label out of a match so it can be checked against
// the self-promo list.
// Spoken addresses arrive with the words apart — "giant bomb.com slash join",
// "kind of funny dot com" — so up to three preceding words are captured and
// the spaces removed before comparison. Capturing one word gave the host as
// "bomb", which matched nothing in the self-promo list, and the show's own
// link was reported as a third-party ad on every Game Mess Mornings episode.
var hostRe = regexp.MustCompile(`(?i)\b((?:[a-z0-9-]+[ ]){0,2}[a-z0-9-]{2,}) ?(?:\.|dot )(?:com|co\.uk|net|org|io|tv|tech|co|shop|store|app|nyc|us|xyz|gg|fm)\b`)

// squash reduces a name to letters and digits so "giant bomb" and "giantbomb"
// compare equal.
func squash(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func isSelfPromo(text string) bool {
	m := hostRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	host := strings.ToLower(m[1])
	for _, h := range selfPromoHosts {
		if host == h || strings.Contains(host, h) {
			return true
		}
	}
	return false
}

// AuditLeftovers reports third-party ad evidence outside the spans that were
// cut. extraSelf lets a caller add the show's own domains, which are not ads.
func AuditLeftovers(segs []Segment, cut []Span, extraSelf []string) []Leftover {
	inCut := func(t float64) bool {
		for _, s := range cut {
			if t >= s.Start-2 && t <= s.End+2 {
				return true
			}
		}
		return false
	}
	var out []Leftover
	seen := map[string]bool{}
	report := func(at float64, what, text string) {
		key := fmt.Sprintf("%s@%d", what, int(at/60))
		if seen[key] {
			return
		}
		seen[key] = true
		if len(text) > 90 {
			text = text[:90]
		}
		out = append(out, Leftover{At: at, What: what, Text: strings.TrimSpace(text)})
	}

	for i, s := range segs {
		if inCut(s.Start) {
			continue
		}
		// A spoken address straddles exactly one boundary — "...at
		// merchant.tech" then "Slash giant bomb" — so the evidence is this
		// segment plus the next, and no more. A ±10s window instead swept in
		// unrelated speech: on the real library it reported "Maybe I am one of
		// the lucky few to enjoy God's sun" as a vanity address because a link
		// happened to be spoken nearby, and the quoted text made the finding
		// impossible to judge.
		w := s.Text
		if i+1 < len(segs) && adjacent(segs, i, i+1) {
			w += " " + segs[i+1].Text
		}
		for _, p := range auditPatterns {
			if !p.re.MatchString(w) {
				continue
			}
			if p.name == "vanity address" && !hasThirdPartyHost(w, extraSelf) {
				continue // only the show's own links are in there
			}
			// quote the pair we actually matched, so the log says why
			report(s.Start, p.name, w)
		}
	}

	// A cut that begins well after the hand-off into the break has left the
	// opening of the read in the episode. That is the "mangled fragment"
	// failure, and it leaves no address behind to find, so it needs its own
	// check: twice now a span started 45-65s into an ad and the listener got
	// the setup with the ad's body removed.
	for _, sp := range cut {
		for i := range segs {
			if segs[i].End > sp.Start {
				break
			}
			if sp.Start-segs[i].End > 180 || !matches(breakOpenPatterns, segs[i].Text) {
				continue
			}
			if gap := sp.Start - segs[i].End; gap > 15 {
				report(segs[i].End, "cut starts late",
					fmt.Sprintf("%.0fs between the break hand-off and the cut", gap))
			}
		}
	}
	return out
}

// hasThirdPartyHost reports whether any address in the text belongs to someone
// other than the show. Each match is checked separately: a window can hold a
// Patreon plug AND an advertiser, and judging it by the first address found
// would let the advertiser through.
func hasThirdPartyHost(text string, extraSelf []string) bool {
	for _, m := range hostRe.FindAllStringSubmatch(text, -1) {
		// The capture may include leading words that are not part of the name
		// ("by going to giantbom"), so every suffix is tried. Comparing the
		// whole phrase only, the show's own address came back as third-party
		// whenever the sentence ran into it.
		words := strings.Fields(m[1])
		self := false
		for k := range words {
			if isKnownSelf(squash(strings.Join(words[k:], "")), extraSelf) {
				self = true
				break
			}
		}
		if !self {
			return true
		}
	}
	return false
}

// isKnownSelf reports whether a host is one the show uses for itself. The
// comparison runs both ways, because the show's name may be longer than the
// host ("gamemessmorningsgiantbomb" contains "giantbomb") or shorter.
func isKnownSelf(host string, extraSelf []string) bool {
	for _, h := range selfPromoHosts {
		if host == h || strings.Contains(host, h) {
			return true
		}
	}
	for _, h := range extraSelf {
		hh := squash(h)
		if len(hh) < 4 {
			continue
		}
		if strings.Contains(host, hh) || strings.Contains(hh, host) {
			return true
		}
	}
	return false
}

// DescribeLeftovers renders an audit result for the event log.
func DescribeLeftovers(l []Leftover) string {
	parts := make([]string, 0, len(l))
	for _, x := range l {
		parts = append(parts, fmt.Sprintf("%s at %dm%02ds (%q)",
			x.What, int(x.At)/60, int(x.At)%60, x.Text))
	}
	return strings.Join(parts, "; ")
}
