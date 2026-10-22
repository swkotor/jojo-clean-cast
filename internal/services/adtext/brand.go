package adtext

// Detecting a read ad by the brand name being hammered.
//
// The sponsor-vocabulary rules miss ads that are performed rather than read.
// A Giant Bomb episode carried a 75s Merchantech spot played as a bit between
// the hosts — no "sponsor", no "brought to you by", no "ad-free", nothing the
// other rules look for, and the only URL arrived 16s after the only generic
// signal, too far apart to count as co-occurring. It survived untouched.
//
// What it did have is the shape every read ad has: the advertiser's name, over
// and over, inside one short stretch, and nowhere else in the episode.
//
//	1474  Merchant Tech can help you do that
//	1478  Not anymore. Merchant Tech is here to help support independent...
//	1497  But Merchantech will, they have white glove customer service
//	1505  I hope Merchantech will be my new best friend too
//	1518  my BFF merchant tech is here for you
//	1524  Hit up our friends over at merchant.tech slash giant bomb
//
// Six mentions in fifty seconds; none in the other fifty-four minutes. A show
// discussing a game or company repeats its name too, but across the episode,
// not sealed inside a minute — which is what the concentration test below
// measures. A call to action inside the window is still required, so a news
// item that happens to dwell on one name is not enough.

import (
	"regexp"
	"sort"
	"strings"
)

// how tightly the mentions must cluster, and how many there must be
const (
	brandWindow     = 120.0
	brandMinHits    = 4
	brandConcentrat = 0.8 // share of all mentions that must fall in the window
	brandMinLen     = 5   // ignore short words; brand names are rarely shorter
)

var wordRe = regexp.MustCompile(`[a-z][a-z0-9]{3,}`)

// Words that recur in bursts in ordinary podcast talk and would otherwise look
// like brands. The concentration test rejects most things on its own; these are
// the ones that genuinely do cluster inside a single topic.
var brandStop = map[string]bool{
	"about": true, "actually": true, "after": true, "again": true, "against": true,
	"already": true, "always": true, "anything": true, "around": true, "because": true,
	"before": true, "being": true, "better": true, "between": true, "business": true,
	"could": true, "course": true, "different": true, "doing": true, "enough": true,
	"episode": true, "especially": true, "everyone": true, "everything": true,
	"exactly": true, "first": true, "friend": true, "friends": true, "games": true,
	"gonna": true, "great": true, "guess": true, "happened": true, "having": true,
	"interesting": true, "kinda": true, "likely": true, "little": true, "maybe": true,
	"mean": true, "might": true, "money": true, "never": true, "nothing": true,
	"other": true, "people": true, "pretty": true, "probably": true, "question": true,
	"really": true, "right": true, "same": true, "season": true, "series": true,
	"should": true, "since": true, "something": true, "sorry": true, "still": true,
	"stuff": true, "support": true, "sure": true, "takes": true, "talking": true,
	"thank": true, "thanks": true, "that": true, "their": true, "there": true,
	"these": true, "thing": true, "things": true, "think": true, "those": true,
	"thought": true, "through": true, "time": true, "together": true, "video": true,
	"watch": true, "weeks": true, "went": true, "were": true, "what": true,
	"when": true, "where": true, "which": true, "while": true, "with": true,
	"would": true, "years": true, "your": true,
}

// a destination or instruction, which a sponsor read always carries somewhere
var brandCallToAction = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z0-9-]+ ?(\.|dot )(com|co\.uk|net|org|io|tv|tech|co|shop|store) ?(/|slash) ?[a-z]`),
	regexp.MustCompile(`(?i)\b(friends|folks) (over )?at\b`),
	regexp.MustCompile(`(?i)\b(hit up|check out|head (on )?(over )?to|go to|sign up|visit)\b`),
	regexp.MustCompile(`(?i)\b(promo|discount|offer) code\b|\buse code\b`),
}

// domainRe finds spoken and written addresses: "merchant.tech slash giant
// bomb", "patreon dot com".
var domainRe = regexp.MustCompile(`(?i)\b([a-z0-9-]{3,})\s?(?:\.|dot )(?:com|co\.uk|net|org|io|tv|tech|co|shop|store|app|gg|fm)\b`)

// namesADomain reports whether tok is the label of an address mentioned in the
// text. Matching is loose in both directions because transcription splits and
// joins brand names unpredictably — the same spot gave "Merchant Tech",
// "Merchantech" and "merchant.tech".
func namesADomain(tok, text string) bool {
	for _, m := range domainRe.FindAllStringSubmatch(text, -1) {
		label := strings.ToLower(m[1])
		if label == tok || strings.Contains(label, tok) || strings.Contains(tok, label) {
			return true
		}
	}
	return false
}

// FindBrandReads returns spans where one name is repeated inside a short
// stretch and barely used elsewhere, and the stretch asks the listener to do
// something.
func FindBrandReads(segs []Segment, total float64) []Span {
	if len(segs) < 4 {
		return nil
	}
	// Which segments every word occurs in. Segment INDEX, not timestamp: a
	// mention's time is its segment's start, so a cluster ending on the last
	// mention stopped just before the segment that contained it — and that was
	// the segment carrying "merchant.tech", the one piece of evidence the
	// domain check needs. The read went undetected.
	at := map[string][]int{}
	for i := range segs {
		for _, w := range wordRe.FindAllString(strings.ToLower(segs[i].Text), -1) {
			if len(w) < brandMinLen || brandStop[w] {
				continue
			}
			at[w] = append(at[w], i)
		}
	}

	var out []Span
	for tok, idx := range at {
		if len(idx) < brandMinHits {
			continue
		}
		sort.Ints(idx)
		// tightest run of brandMinHits mentions
		best, bi, bj := brandWindow+1, 0, 0
		for i := 0; i+brandMinHits-1 < len(idx); i++ {
			j := i + brandMinHits - 1
			if d := segs[idx[j]].End - segs[idx[i]].Start; d < best {
				best, bi, bj = d, i, j
			}
		}
		if best > brandWindow {
			continue
		}
		// grow the cluster over every mention still inside the window
		first, last := idx[bi], idx[bj]
		for _, k := range idx {
			if k > last && segs[k].End-segs[first].Start <= brandWindow {
				last = k
			}
		}
		lo, hi := segs[first].Start, segs[last].End
		inside := 0
		for _, k := range idx {
			if k >= first && k <= last {
				inside++
			}
		}
		if float64(inside)/float64(len(idx)) < brandConcentrat {
			continue // the name is used across the episode: it is a topic
		}
		a, b := snap(segs, lo, hi)
		w := textIn(segs, a, b)
		// The repeated name must also be the thing being linked to. Without
		// this the detector fired on any subject discussed in a burst —
		// "london", "player", "security", "gamestop", "magazine" — and the
		// resulting pile of spans tripped the total-length safety net, which
		// then correctly refused to cut anything at all, including the real
		// ad. A read ad says its advertiser's address; a conversation about
		// a topic does not.
		if !namesADomain(tok, w) {
			continue
		}
		if !matches(brandCallToAction, w) {
			continue // repetition without a pitch is just a subject
		}
		if b-a < 8 || b-a > 200 {
			continue
		}
		out = append(out, Span{Start: a, End: b, Reason: "brand read: " + tok})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return mergeSpans(out)
}
