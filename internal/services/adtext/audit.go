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
var hostRe = regexp.MustCompile(`(?i)\b([a-z0-9-]{3,}) ?(?:\.|dot )(?:com|co\.uk|net|org|io|tv|tech|co|shop|store|app|nyc|us|xyz|gg|fm)\b`)

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
		// A spoken address straddles segments — "...at merchant.tech" then
		// "Slash giant bomb" — so the evidence is looked for in a window, not
		// in one segment. Checking segments alone missed the very ad this
		// audit was written for.
		w := window(segs, i, 10)
		for _, p := range auditPatterns {
			if !p.re.MatchString(w) {
				continue
			}
			if p.name == "vanity address" && !hasThirdPartyHost(w, extraSelf) {
				continue // only the show's own links are in there
			}
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
		host := strings.ToLower(m[1])
		self := false
		for _, h := range selfPromoHosts {
			if host == h || strings.Contains(host, h) {
				self = true
				break
			}
		}
		for _, h := range extraSelf {
			if h != "" && strings.Contains(host, strings.ToLower(h)) {
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

func matchesAnyHost(text string, hosts []string) bool {
	m := hostRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	host := strings.ToLower(m[1])
	for _, h := range hosts {
		if h != "" && strings.Contains(host, strings.ToLower(h)) {
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
