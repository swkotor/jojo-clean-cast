package adtext

// A local language model reads the transcript in overlapping windows and
// reports host-read ads that the rules missed or only partly caught.
//
// Joel's decision: the model may add cuts on its own, but only when it is
// highly confident and the cut is a plausible ad length. Everything else it
// says is logged and ignored. If the model is unreachable, or returns nothing
// usable, the episode is cut on the rules alone, exactly as before.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	llmWindow       = 8 * 60.0 // seconds of transcript per request
	llmOverlap      = 60.0     // seconds shared with the next window
	llmMinConfident = 0.9      // below this, the model's suggestion is ignored
	llmMaxRequests  = 40       // a runaway transcript cannot make unbounded calls
)

// LLMConfig says where the model is. Empty URL means the pass is off.
type LLMConfig struct {
	URL    string // Ollama base URL, e.g. http://192.168.2.44:11434
	Model  string // e.g. qwen2.5-coder:14b
	Client *http.Client
}

// LLMConfigFromEnv reads ADTEXT_LLM_URL and ADTEXT_LLM_MODEL. The pass is on
// only when ADTEXT_LLM_URL is set, so an install without a model is unaffected.
func LLMConfigFromEnv() LLMConfig {
	model := strings.TrimSpace(os.Getenv("ADTEXT_LLM_MODEL"))
	if model == "" {
		model = "qwen2.5-coder:14b"
	}
	return LLMConfig{
		URL:    strings.TrimRight(strings.TrimSpace(os.Getenv("ADTEXT_LLM_URL")), "/"),
		Model:  model,
		Client: &http.Client{Timeout: 4 * time.Minute},
	}
}

type llmAd struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type llmAnswer struct {
	Ads []llmAd `json:"ads"`
}

// llmWindows splits the episode into overlapping windows, [start, end) pairs.
func llmWindows(total float64) [][2]float64 {
	var out [][2]float64
	step := llmWindow - llmOverlap
	for start := 0.0; start < total; start += step {
		end := start + llmWindow
		if end > total {
			end = total
		}
		out = append(out, [2]float64{start, end})
		if end >= total {
			break
		}
	}
	if len(out) > llmMaxRequests {
		out = out[:llmMaxRequests]
	}
	return out
}

// windowTranscript renders the segments that fall inside [a, b) with their
// start times, so the model can answer in seconds.
func windowTranscript(segs []Segment, a, b float64) string {
	var sb strings.Builder
	for _, s := range segs {
		if s.End <= a || s.Start >= b || strings.TrimSpace(s.Text) == "" {
			continue
		}
		fmt.Fprintf(&sb, "[%.0f] %s\n", s.Start, strings.TrimSpace(s.Text))
	}
	return sb.String()
}

const llmSystemPrompt = `You check a podcast transcript for advertising read by the host.
An ad read is a paid or promotional message: a sponsor, a product or service pitch,
a promo code, a call to visit a website or sign up, or a "brought to you by" spot.
A host talking about their own show, a guest's work, news, or a passing mention of a
product in a non-promotional way is NOT an ad.
Each line starts with its start time in seconds in square brackets.
Reply with JSON only, in exactly this shape:
{"ads":[{"start":<seconds>,"end":<seconds>,"confidence":<0 to 1>,"reason":"<short>"}]}
Use the first line's time as the start and the last line's time as the end of the ad.
If there are no ads, reply {"ads":[]}.`

// askModel sends one window and returns the parsed answer.
func askModel(cfg LLMConfig, transcript string) (llmAnswer, error) {
	body, _ := json.Marshal(map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": llmSystemPrompt},
			{"role": "user", "content": transcript},
		},
		"stream":     false,
		"format":     "json",
		"keep_alive": 0, // the GPU is shared with Plex, Immich and the transcriber
		"options":    map[string]any{"temperature": 0, "num_ctx": 8192},
	})
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Minute}
	}
	resp, err := client.Post(cfg.URL+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return llmAnswer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return llmAnswer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return llmAnswer{}, fmt.Errorf("model returned %d", resp.StatusCode)
	}
	var env struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return llmAnswer{}, err
	}
	var ans llmAnswer
	if err := json.Unmarshal([]byte(env.Message.Content), &ans); err != nil {
		return llmAnswer{}, fmt.Errorf("unreadable answer: %w", err)
	}
	return ans, nil
}

// parseLLMAds keeps only the suggestions that may be cut without review: a
// confident answer, a sane span inside the episode, and a plausible length.
// Boundaries are snapped to the transcript's own segment edges, so a cut
// never lands mid-sentence.
func parseLLMAds(ans llmAnswer, segs []Segment, total float64, o Options) []Span {
	var out []Span
	for _, ad := range ans.Ads {
		if ad.Confidence < llmMinConfident {
			continue
		}
		a, b := ad.Start, ad.End
		if b <= a || a < 0 || b > total+1 {
			continue
		}
		a, b = snap(segs, a, b)
		span := Span{Start: a, End: b, Reason: "llm: " + strings.TrimSpace(ad.Reason)}
		if span.Seconds() < o.MinSpan || span.Seconds() > o.MaxSpan {
			continue
		}
		out = append(out, span)
	}
	return out
}

// LLMSpans runs the model over every window and returns the spans it may cut.
// Errors on one window are logged by the caller and skipped; the rest still
// count. A result that would break the episode's ad cap is dropped whole.
func LLMSpans(cfg LLMConfig, segs []Segment, total float64, o Options) ([]Span, []string) {
	if cfg.URL == "" || len(segs) == 0 || total <= 0 {
		return nil, nil
	}
	var spans []Span
	var problems []string
	for _, w := range llmWindows(total) {
		text := windowTranscript(segs, w[0], w[1])
		if strings.TrimSpace(text) == "" {
			continue
		}
		ans, err := askModel(cfg, text)
		if err != nil {
			problems = append(problems, fmt.Sprintf("window %.0f-%.0fs: %v", w[0], w[1], err))
			continue
		}
		spans = append(spans, parseLLMAds(ans, segs, total, o)...)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	// Overlapping windows report the same ad twice; merge them first.
	spans = mergeSpans(spans)
	// Merging can join suggestions into a span longer than any single ad, so the
	// length rule is applied again to the merged result.
	kept := spans[:0]
	for _, s := range spans {
		if s.Seconds() > o.MaxSpan {
			problems = append(problems, fmt.Sprintf("merged suggestion %.0f-%.0fs is too long; ignored", s.Start, s.End))
			continue
		}
		kept = append(kept, s)
	}
	spans = kept
	var sum float64
	for _, s := range spans {
		sum += s.Seconds()
	}
	if sum > maxTotalFor(total, o) {
		problems = append(problems, fmt.Sprintf("model suggested %.0fs, over the episode cap; ignored", sum))
		return nil, problems
	}
	return spans, problems
}

// MergeLLM adds the model's confident spans to the rule-based ones.
func MergeLLM(rules, llm []Span) []Span {
	if len(llm) == 0 {
		return rules
	}
	return mergeSpans(append(append([]Span{}, rules...), llm...))
}
