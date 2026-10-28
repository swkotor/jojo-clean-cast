package adtext

// Trimming an inserted pre-roll that is in the wrong language.
//
// The differential pass removes ads that differ between downloads, but a slot
// filled with the SAME creative in every fetch is invisible to it. That is how
// a Giant Bombcast episode shipped with 48s of a Dutch ad still on the front:
// three downloads shared 99.5% of their frames, so only 30s of the 78s
// pre-roll was removed.
//
// A stretch of another language at the start of an English show is not a
// judgement call — no keyword list is needed, and there is no risk of mistaking
// it for content. The show's own language is taken from the middle of the
// episode rather than assumed, so a genuinely Dutch podcast is unaffected.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/labstack/gommon/log"
)

const (
	prerollProbe  = 15.0  // granularity of the first sweep
	prerollRefine = 5.0   // granularity of the boundary search
	prerollMax    = 240.0 // never treat more than this as pre-roll
	langMinProb   = 0.6   // below this the probe is not trusted
)

type langResult struct {
	Language    string  `json:"language"`
	Probability float64 `json:"probability"`
}

var langClient = &http.Client{Timeout: 3 * time.Minute}

// probeLanguage asks the sidecar what language is spoken in one stretch.
func probeLanguage(relPath string, from, to float64) (langResult, error) {
	base := remoteURL()
	if base == "" {
		return langResult{}, fmt.Errorf("no sidecar configured")
	}
	u := fmt.Sprintf("%s/language?path=%s&from=%.1f&to=%.1f",
		strings.TrimRight(base, "/"), url.QueryEscape(relPath), from, to)
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return langResult{}, err
	}
	resp, err := langClient.Do(req)
	if err != nil {
		return langResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return langResult{}, fmt.Errorf("sidecar returned %d", resp.StatusCode)
	}
	var r langResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return langResult{}, err
	}
	return r, nil
}

// ForeignPreroll returns how many seconds at the front of the file are spoken
// in a language other than the show's own, or 0.
func ForeignPreroll(relPath string, total float64) float64 {
	if total < 300 {
		return 0 // too short to tell a pre-roll from the show
	}
	// The show's language, read from the middle so a long pre-roll cannot
	// define it.
	mid, err := probeLanguage(relPath, total/2, total/2+30)
	if err != nil {
		log.Warnf("[PREROLL] %s: %v", relPath, err)
		return 0
	}
	if mid.Probability < 0.9 {
		return 0 // unsure what the show even is; do nothing
	}

	head, err := probeLanguage(relPath, 0, prerollProbe)
	if err != nil || head.Language == mid.Language || head.Probability < langMinProb {
		return 0 // starts in the show's own language: nothing to trim
	}

	// Walk forward to the first window that is the show's language.
	end := prerollProbe
	for end < prerollMax {
		r, err := probeLanguage(relPath, end, end+prerollProbe)
		if err != nil {
			return 0
		}
		if r.Language == mid.Language && r.Probability >= langMinProb {
			break
		}
		end += prerollProbe
	}
	if end >= prerollMax {
		log.Warnf("[PREROLL] %s: foreign audio past %.0fs — leaving it alone", relPath, prerollMax)
		return 0
	}

	// Refine backwards: the boundary is somewhere inside the last foreign
	// window, and cutting a whole window would take the opening of the show.
	for t := end - prerollProbe + prerollRefine; t < end; t += prerollRefine {
		r, err := probeLanguage(relPath, t, t+prerollRefine*2)
		if err != nil {
			break
		}
		if r.Language == mid.Language && r.Probability >= langMinProb {
			end = t
			break
		}
	}
	log.Infof("[PREROLL] %s: %.0fs of %s before the show's %s", relPath, end, head.Language, mid.Language)
	return end
}
