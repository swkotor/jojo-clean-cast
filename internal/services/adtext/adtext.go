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
func Transcribe(configDir, audioPath string, threads int) ([]Segment, error) {
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
	regexp.MustCompile(`(?i)\b(head|go|jump|pop) (on )?(over )?to\b`),
	regexp.MustCompile(`(?i)\b(sign|signing) up\b`),
	regexp.MustCompile(`(?i)\b(visit|check out)\b.{0,30}\.(com|co\.uk|net|org|tv|fm|io)\b`),
	regexp.MustCompile(`(?i)\b(use|with) (the )?(promo |discount )?code\b`),
	regexp.MustCompile(`(?i)\bslash [a-z]`),
	regexp.MustCompile(`(?i)\b(dot|\.)(com|co\.uk|net|org)\b.{0,20}\bslash\b`),
	regexp.MustCompile(`(?i)\b(subscribe|support) (to |the )?(us|the show|this podcast)\b`),
	regexp.MustCompile(`(?i)\b(per|a) month\b`),
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
	regexp.MustCompile(`(?i)\b(merch|membership|members)\b`),
	// The tail of a membership pitch ("at our higher tiers you'll also get...")
	// carries no URL and no sponsor noun, so without this the span stopped
	// short and left the last few seconds of the ad in.
	regexp.MustCompile(`(?i)\b(tier|tiers|bonus episodes|early access)\b`),
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
	strong := make([]bool, len(segs))
	for i, s := range segs {
		strong[i] = matches(sponsorPatterns, s.Text) && matches(ctaPatterns, s.Text)
	}
	// A neighbour of a strong segment counts if it carries either signal: ad
	// reads run over several segments and the URL may land in only one.
	member := make([]bool, len(segs))
	for i := range segs {
		if !strong[i] {
			continue
		}
		member[i] = true
		for _, j := range []int{i - 1, i + 1} {
			if j >= 0 && j < len(segs) &&
				(matches(sponsorPatterns, segs[j].Text) || matches(ctaPatterns, segs[j].Text)) {
				member[j] = true
			}
		}
	}

	var spans []Span
	for i := 0; i < len(segs); i++ {
		if !member[i] {
			continue
		}
		j := i
		for j+1 < len(segs) && member[j+1] {
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
