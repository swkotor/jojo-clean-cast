// Package adtext finds HOST-READ ads by reading what is being said.
//
// The differential stripper removes ads a host STITCHES IN per listener, by
// comparing downloads. It cannot touch an ad the presenters read aloud during
// recording: that is part of the master, identical in every copy. SponsorBlock
// would cover it on YouTube, but only where somebody has submitted the segment,
// and for several of these shows nobody has.
//
// So this transcribes the episode locally (whisper.cpp, on CPU) and looks for
// the shape of an ad read. Transcription is a mechanical, checkable task and is
// exactly the kind of work worth running on the box rather than guessing at.
//
// Deciding what is an ad, however, is a judgement, and a wrong decision deletes
// part of the show. That is why this does NOT ask a language model; it uses
// explicit rules with conservative gates, so every cut can be explained and
// reviewed. The rules were written against a real failure: in one VGC episode
// the genuine ad spans 13s-36s ("head to patreon.com/... three pounds a month"),
// but the hosts also say "still on the Patreon" at 7:29 in ordinary
// conversation. A keyword match alone would have cut the show.
package adtext

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/labstack/gommon/log"
)

// Segment is one transcribed span.
type Segment struct {
	Start float64
	End   float64
	Text  string
}

// Span is a stretch of the episode judged to be an ad.
type Span struct {
	Start  float64
	End    float64
	Reason string
}

func (s Span) Seconds() float64 { return s.End - s.Start }

const modelFile = "ggml-base.en.bin"
const modelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/" + modelFile

// ModelPath is where the speech model is cached (next to the rest of appdata,
// so it survives image rebuilds and is downloaded only once).
func ModelPath(configDir string) string {
	return filepath.Join(configDir, "whisper", modelFile)
}

// Available reports whether transcription can run at all.
func Available(configDir string) bool {
	if _, err := exec.LookPath("whisper-cli"); err != nil {
		return false
	}
	st, err := os.Stat(ModelPath(configDir))
	return err == nil && st.Size() > 1_000_000
}

// EnsureModel downloads the speech model on first use.
func EnsureModel(configDir string) error {
	dst := ModelPath(configDir)
	if st, err := os.Stat(dst); err == nil && st.Size() > 1_000_000 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	log.Infof("[ADTEXT] Downloading speech model (~142MB), one time only")
	tmp := dst + ".part"
	cmd := exec.Command("wget", "-q", "-O", tmp, modelURL)
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("downloading speech model: %w", err)
	}
	return os.Rename(tmp, dst)
}

type whisperOut struct {
	Transcription []struct {
		Timestamps struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"timestamps"`
		Text string `json:"text"`
	} `json:"transcription"`
}

// hhmmss parses whisper's "00:01:02,340".
func hhmmss(s string) float64 {
	s = strings.Replace(strings.TrimSpace(s), ",", ".", 1)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, _ := strconv.ParseFloat(parts[0], 64)
	m, _ := strconv.ParseFloat(parts[1], 64)
	sec, _ := strconv.ParseFloat(parts[2], 64)
	return h*3600 + m*60 + sec
}

// Transcribe converts the episode to 16kHz mono and runs whisper over it.
//
// Runs on CPU deliberately. The box has a GPU, but it is shared with Plex and
// immich, and a model resident in VRAM would collide with a transcode; this
// work is bursty background processing where ~14x realtime is plenty.
// transcribing is the single transcription slot. whisper saturates every core
// it is given, so two at once take twice as long each and leave nothing for
// serving audio — observed with a download-path run and a backlog run
// overlapping. Serialising costs nothing in throughput and keeps the box
// responsive.
var transcribing = make(chan struct{}, 1)

func Transcribe(configDir, audioPath string, threads int) ([]Segment, error) {
	transcribing <- struct{}{}
	defer func() { <-transcribing }()

	// Prefer the GPU sidecar when one is configured. whisper.cpp here is not
	// just slower, it loses speech: on a 66-minute episode it returned 313
	// segments against the sidecar's 958, and across the 50s holding a
	// membership pitch it produced the single line "Are you an influencer?".
	// Detection cannot find an ad that is missing from the transcript.
	if segs, err := transcribeRemote(audioPath); err == nil {
		return segs, nil
	} else if remoteURL() != "" {
		log.Warnf("[ADTEXT] sidecar unavailable (%v); falling back to whisper.cpp", err)
	}

	if err := EnsureModel(configDir); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "adtext")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	wav := filepath.Join(work, "a.wav")
	ff := exec.Command("ffmpeg", "-v", "error", "-i", audioPath,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav, "-y")
	if out, err := ff.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("decoding audio: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if threads < 1 {
		threads = 8
	}
	base := filepath.Join(work, "out")
	wc := exec.Command("whisper-cli",
		"-m", ModelPath(configDir), "-f", wav,
		"-t", strconv.Itoa(threads), "-np", "-oj", "-of", base)
	if out, err := wc.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("transcribing: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		return nil, err
	}
	var w whisperOut
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	segs := make([]Segment, 0, len(w.Transcription))
	for _, t := range w.Transcription {
		segs = append(segs, Segment{
			Start: hhmmss(t.Timestamps.From),
			End:   hhmmss(t.Timestamps.To),
			Text:  strings.TrimSpace(t.Text),
		})
	}
	return segs, nil
}

// A call to action is what separates an ad read from talking ABOUT a sponsor.
// "still on the Patreon" is conversation; "head to patreon.com/x" is an ad.
var ctaPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(head|go|jump|pop) (on )?(over )?to\b.{0,40}(\.(com|co\.uk|net|org|io|tv)\b|\bdot com\b|\bslash\b|\bstore\b)`),
	regexp.MustCompile(`(?i)\b(sign|signing) up\b`),
	regexp.MustCompile(`(?i)\b(visit|check out)\b.{0,30}\.(com|co\.uk|net|org|tv|fm|io)\b`),
	regexp.MustCompile(`(?i)\b(use|with) (the )?(promo |discount )?code\b`),
	regexp.MustCompile(`(?i)\bslash [a-z]`),
	regexp.MustCompile(`(?i)\b(dot|\.)(com|co\.uk|net|org)\b.{0,20}\bslash\b`),
	regexp.MustCompile(`(?i)\b(subscribe|support) (to |the )?(us|the show|this podcast)\b`),
	regexp.MustCompile(`(?i)([$£€]\s?\d+|\b\d+ (pounds|dollars|euros|bucks)) ?(a|per) month\b|\bfor as little as\b`),
	regexp.MustCompile(`(?i)\b(free trial|first month free|percent off|% off)\b`),
}

// Sponsor words on their own are far too weak to cut on.
var sponsorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ad|advert)[ -]?free\b`),
	regexp.MustCompile(`(?i)\bpatreon\b`),
	regexp.MustCompile(`(?i)\b(sponsor|sponsored|sponsors|sponsorship)\b`),
	regexp.MustCompile(`(?i)\bbrought to you by\b`),
	regexp.MustCompile(`(?i)\b(this episode|today's episode) is (supported|sponsored)\b`),
	regexp.MustCompile(`(?i)\bthanks? to our (sponsor|supporters)\b`),
	regexp.MustCompile(`(?i)\b(membership|members)\b|\bmerch (store|shop)\b`),
	// The tail of a membership pitch ("at our higher tiers you'll also get...")
	// carries no URL and no sponsor noun, so without this the span stopped
	// short and left the last few seconds of the ad in.
	regexp.MustCompile(`(?i)\b(higher|top|upper) tiers?\b|\btiers? you'?ll\b|\b(bonus episodes|early access)\b`),
}

func matches(pats []*regexp.Regexp, s string) bool {
	for _, p := range pats {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// Options tunes how aggressive detection is.
type Options struct {
	MinSpan     float64 // ignore anything shorter than this
	MaxSpan     float64 // a "span" longer than this is not an ad read
	MaxTotal    float64 // never cut more than this in one episode
	MaxFraction float64 // ...nor more than this share of it
}

// signalWindow is how far either side of a segment the sponsor and
// call-to-action signals may sit and still count as co-occurring. About one
// sentence of speech: wide enough to span whisper's fragmentation, narrow
// enough that a sponsor noun here and an unrelated "check out" a minute away
// do not combine.
const signalWindow = 10.0

// maxJoinGap is the largest silence two segments may be separated by and still
// be treated as one continuous read.
//
// Neighbouring INDEX is not neighbouring TIME. A transcript's segments skip
// over anything unspoken, so segment n+1 can be minutes after segment n — and
// joining across such a gap once produced a 456s "ad" that spanned from the
// intro to a passing Patreon mention seven minutes later, which the span
// length check then discarded, losing the real ad with it.
const maxJoinGap = 10.0

// bridgeSpan is the largest stretch of keyword-free speech that may sit inside
// a single read without ending it.
const bridgeSpan = 60.0

// bridgeGap allows longer pauses inside a read than maxJoinGap does when
// extending one. A pitch pauses — Easy Allies' has 20s of patron shout-outs
// between "patreon.com slash easy allies" and "now come join us on Patreon" —
// and the tighter gap split one 54s read into a 19s fragment. Safe because
// bridging needs a confirmed member at BOTH ends and is capped by bridgeSpan.
const bridgeGap = 25.0

func adjacent(segs []Segment, i, j int) bool {
	return segs[j].Start-segs[i].End <= maxJoinGap
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// window returns the text of every segment overlapping a span of signalWindow
// seconds either side of segs[i].
func window(segs []Segment, i int, radius float64) string {
	a, b := segs[i].Start-radius, segs[i].End+radius
	out := ""
	for _, s := range segs {
		if s.End > a && s.Start < b {
			out += " " + s.Text
		}
	}
	return out
}

// RulesRevision identifies the current detection rules. An episode records
// the revision it was processed under, so improving the rules re-opens the
// episodes that were scanned with the weaker ones.
//
// BUMP THIS whenever a change makes the rules find something they previously
// missed. Without it, "already transcribed" meant "never looked at again": VGC
// episodes scanned on 11 Oct, hours before the break-detection rules landed,
// kept a 15s Patreon pre-roll the current rules detect cleanly.
//
//	1 - sponsor scoring only
//	2 - break hand-off detection (breaks.go), transcript-wide matching
//	3 - signals matched in a window, not per segment
//	4 - tightened "go to", "a month", merch and tier patterns
//	5 - brand-repetition reads (brand.go)
const RulesRevision = 5

func DefaultOptions() Options {
	return Options{MinSpan: 8, MaxSpan: 180, MaxTotal: 300, MaxFraction: 0.15}
}

// FindAds locates ad reads.
//
// A segment only COUNTS when it carries both a sponsor word and a call to
// action, or is adjacent to one that does — that pairing is what distinguishes
// an ad read from a mention. Qualifying segments are then merged into runs, and
// a run must clear MinSpan to be cut, so an isolated remark can never qualify.
func FindAds(segs []Segment, total float64, o Options) []Span {
	if len(segs) == 0 {
		return nil
	}
	// Two separate questions, deliberately kept apart.
	//
	// "Is an ad read happening around here?" is answered from a WINDOW, because
	// whisper's segmentation is arbitrary and varies hugely between shows —
	// Easy Allies comes back in 2-6s fragments, which split
	// "patreon.com slash easy allies" from "two days early and ad free" into
	// different segments and left a 54s read looking like one 7s blip.
	//
	// "Is THIS segment part of it?" is answered from the segment's own text.
	// Using the window for both made every show segment beside an ad look
	// strong, because its window reached back into the ad: the VGC span grew
	// from 13.5-35.8 to 0-41 and swallowed the episode intro.
	context := make([]bool, len(segs))
	own := make([]bool, len(segs))
	for i := range segs {
		w := window(segs, i, signalWindow)
		context[i] = matches(sponsorPatterns, w) && matches(ctaPatterns, w)
		own[i] = matches(sponsorPatterns, segs[i].Text) || matches(ctaPatterns, segs[i].Text)
	}
	member := make([]bool, len(segs))
	for i := range segs {
		member[i] = context[i] && own[i]
	}

	// Bridge the quiet middle of a read. A pitch spends most of its length on
	// sentences carrying no keyword at all ("you get into the official
	// discord", "you get your name in the credits"), so without this a single
	// read is reported as several sub-MinSpan fragments and discarded.
	// Bounded, and never across a silence, so it cannot join two distant plugs
	// through the show between them.
	for i := 0; i < len(segs); i++ {
		if !member[i] {
			continue
		}
		for j := i + 1; j < len(segs) && segs[j].Start-segs[i].End <= bridgeSpan; j++ {
			if !member[j] {
				continue
			}
			ok := true
			for k := i; k < j; k++ {
				if segs[k+1].Start-segs[k].End > bridgeGap {
					ok = false
					break
				}
			}
			if ok {
				for k := i; k <= j; k++ {
					member[k] = true
				}
			}
			break
		}
	}

	var spans []Span
	for i := 0; i < len(segs); i++ {
		if !member[i] {
			continue
		}
		j := i
		for j+1 < len(segs) && member[j+1] && adjacent(segs, j, j+1) {
			j++
		}
		sp := Span{Start: segs[i].Start, End: segs[j].End}
		sp.Reason = strings.TrimSpace(segs[i].Text)
		if len(sp.Reason) > 70 {
			sp.Reason = sp.Reason[:70]
		}
		if d := sp.Seconds(); d >= o.MinSpan && d <= o.MaxSpan {
			spans = append(spans, sp)
		}
		i = j
	}

	// Scripted "we'll be right back" hand-offs are found structurally rather
	// than by sponsor wording — see breaks.go for why the two are separate.
	spans = mergeSpans(append(spans, FindBreakAnnouncements(segs)...))
	// Ads performed as a bit carry none of the sponsor vocabulary; they are
	// found by the advertiser's name being repeated — see brand.go.
	spans = mergeSpans(append(spans, FindBrandReads(segs, total)...))

	// Final safety net: if the rules somehow select a big share of the episode
	// they are wrong about something, and dropping the audio is worse than
	// leaving the ads in. Cut nothing rather than guess.
	var sum float64
	for _, s := range spans {
		sum += s.Seconds()
	}
	if sum > o.MaxTotal || (total > 0 && sum > o.MaxFraction*total) {
		log.Warnf("[ADTEXT] refusing to cut %.0fs of a %.0fs episode — rules look wrong", sum, total)
		return nil
	}
	return spans
}

// mergeSpans sorts spans and unions any that touch, so the two detectors
// cannot produce overlapping cuts (which would double-count the removed time
// and make the log misleading).
func mergeSpans(in []Span) []Span {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start < in[j].Start })
	out := in[:1]
	for _, sp := range in[1:] {
		last := &out[len(out)-1]
		if sp.Start <= last.End {
			if sp.End > last.End {
				last.End = sp.End
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// Describe renders spans for the event log.
func Describe(spans []Span) string {
	if len(spans) == 0 {
		return "no host-read ads found"
	}
	parts := make([]string, 0, len(spans))
	for _, s := range spans {
		parts = append(parts, fmt.Sprintf("%.0fs@%s", s.Seconds(),
			(time.Duration(s.Start)*time.Second).String()))
	}
	return strings.Join(parts, "; ")
}
