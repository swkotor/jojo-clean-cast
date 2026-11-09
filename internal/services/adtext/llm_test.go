package adtext

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeModel answers each request with the next canned reply.
func fakeModel(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	i := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		reply := `{"ads":[]}`
		if i < len(replies) {
			reply = replies[i]
		}
		i++
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": reply}})
	}))
}

func transcript(n int, step float64) []Segment {
	var segs []Segment
	for i := 0; i < n; i++ {
		start := float64(i) * step
		segs = append(segs, Segment{Start: start, End: start + step, Text: fmt.Sprintf("line %d", i)})
	}
	return segs
}

func TestLLMWindowsCoverEpisodeWithOverlap(t *testing.T) {
	w := llmWindows(1200)
	if len(w) < 2 || w[0][0] != 0 || w[len(w)-1][1] != 1200 {
		t.Fatalf("windows %v do not cover the episode", w)
	}
	for i := 1; i < len(w); i++ {
		if w[i][0] >= w[i-1][1] {
			t.Fatalf("windows %d and %d do not overlap: %v", i-1, i, w)
		}
	}
}

func TestLLMConfidentAdIsAccepted(t *testing.T) {
	srv := fakeModel(t, `{"ads":[{"start":100,"end":160,"confidence":0.95,"reason":"promo code"}]}`)
	defer srv.Close()
	segs := transcript(200, 1)
	spans, problems := LLMSpans(LLMConfig{URL: srv.URL, Model: "m", Client: srv.Client()}, segs, 200, DefaultOptions())
	if len(problems) != 0 || len(spans) != 1 {
		t.Fatalf("want one span, got %v problems %v", spans, problems)
	}
	if spans[0].Start != 100 || spans[0].End != 160 {
		t.Fatalf("span not snapped to segment edges: %+v", spans[0])
	}
}

func TestLLMLowConfidenceIsIgnored(t *testing.T) {
	srv := fakeModel(t, `{"ads":[{"start":100,"end":160,"confidence":0.6,"reason":"maybe"}]}`)
	defer srv.Close()
	spans, _ := LLMSpans(LLMConfig{URL: srv.URL, Model: "m", Client: srv.Client()}, transcript(200, 1), 200, DefaultOptions())
	if len(spans) != 0 {
		t.Fatalf("low confidence must not cut, got %v", spans)
	}
}

func TestLLMImplausibleLengthIsIgnored(t *testing.T) {
	srv := fakeModel(t, `{"ads":[{"start":10,"end":12,"confidence":0.99,"reason":"tiny"},{"start":20,"end":230,"confidence":0.99,"reason":"too long"}]}`)
	defer srv.Close()
	spans, _ := LLMSpans(LLMConfig{URL: srv.URL, Model: "m", Client: srv.Client()}, transcript(300, 1), 300, DefaultOptions())
	if len(spans) != 0 {
		t.Fatalf("too short or too long must not cut, got %v", spans)
	}
}

func TestLLMBadJSONIsReportedNotCut(t *testing.T) {
	srv := fakeModel(t, `not json`)
	defer srv.Close()
	spans, problems := LLMSpans(LLMConfig{URL: srv.URL, Model: "m", Client: srv.Client()}, transcript(200, 1), 200, DefaultOptions())
	if len(spans) != 0 || len(problems) == 0 {
		t.Fatalf("bad answer must be reported and cut nothing: spans %v problems %v", spans, problems)
	}
}

func TestLLMOverCapIsDropped(t *testing.T) {
	// Three confident 100s ads in a 300s episode: 300s is over the 15% cap.
	srv := fakeModel(t,
		`{"ads":[{"start":0,"end":100,"confidence":0.99,"reason":"a"},{"start":100,"end":200,"confidence":0.99,"reason":"b"},{"start":200,"end":300,"confidence":0.99,"reason":"c"}]}`)
	defer srv.Close()
	spans, problems := LLMSpans(LLMConfig{URL: srv.URL, Model: "m", Client: srv.Client()}, transcript(300, 1), 300, DefaultOptions())
	if len(spans) != 0 || len(problems) == 0 {
		t.Fatalf("over-cap suggestions must be dropped whole: spans %v problems %v", spans, problems)
	}
}

func TestLLMUnreachableKeepsRules(t *testing.T) {
	spans, problems := LLMSpans(LLMConfig{URL: "http://127.0.0.1:1", Model: "m"}, transcript(200, 1), 200, DefaultOptions())
	if len(spans) != 0 || len(problems) == 0 {
		t.Fatalf("unreachable model: spans %v problems %v", spans, problems)
	}
	rules := []Span{{Start: 5, End: 40, Reason: "rule"}}
	if got := MergeLLM(rules, nil); len(got) != 1 || got[0].Reason != "rule" {
		t.Fatalf("rules must pass through untouched: %v", got)
	}
}

func containsText(list []string, s string) bool {
	for _, x := range list {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}
