package sponsorblock

// Checking that SponsorBlock segments were actually removed.
//
// The baseline we recorded after a download came from our OWN API call, not
// from what yt-dlp did, so a download where yt-dlp removed nothing still
// recorded the full offered time as skipped. That is how a Kinda Funny Games
// Daily episode sat in the library with 173s of ads while the database claimed
// they were gone, and why nothing ever retried it: the drift check compares
// the stored baseline to the API, and both agreed.
//
// Removed() measures the file instead of trusting either.

import (
	"fmt"
	"ikoyhn/podcast-sponsorblock/internal/services/adtext"
	"io"
	"math"
	"net/http"
	"strings"
)

// Segment is one submitted SponsorBlock range.
type Segment struct {
	Category string
	Start    float64
	End      float64
}

// Fetch returns the segments for the configured categories plus the video
// duration the submitters saw (0 when they disagree or did not report one).
func Fetch(youtubeVideoId string) ([]Segment, float64, error) {
	endURL := SPONSORBLOCK_API_URL + youtubeVideoId
	for _, c := range getCategories() {
		endURL += "&category=" + strings.TrimSpace(c)
	}
	resp, err := sponsorBlockClient.Get(endURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, 0, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	raw, err := unmarshalSponsorBlockResponse(body)
	if err != nil {
		return nil, 0, err
	}
	var segs []Segment
	var dur float64
	for _, r := range raw {
		if len(r.Segment) < 2 {
			continue
		}
		segs = append(segs, Segment{Category: r.Category, Start: r.Segment[0], End: r.Segment[1]})
		if r.VideoDuration > dur {
			dur = r.VideoDuration
		}
	}
	return segs, dur, nil
}

// Outcome of comparing a downloaded file against what SponsorBlock offered.
type Outcome int

const (
	NoSegments Outcome = iota
	Applied            // the file is shorter by about the offered amount
	NotApplied         // the file is full length; yt-dlp removed nothing
	Unclear            // neither length fits; do not touch it
)

func (o Outcome) String() string {
	switch o {
	case NoSegments:
		return "no segments"
	case Applied:
		return "applied"
	case NotApplied:
		return "not applied"
	}
	return "unclear"
}

// Removed works out what really happened, from the length of the file on disk.
//
// tolerance is generous: container overhead and encoder padding move the
// duration by a second or two either way, and a long episode legitimately
// drifts more.
func Removed(fileDuration, videoDuration, offered float64, n int) (Outcome, float64) {
	if n == 0 || offered <= 0 {
		return NoSegments, 0
	}
	if videoDuration <= 0 {
		return Unclear, 0
	}
	tol := math.Max(5, videoDuration*0.002)
	if math.Abs(fileDuration-(videoDuration-offered)) <= tol {
		return Applied, offered
	}
	// "Nothing was removed" has to tolerate a much bigger discrepancy than
	// "it was removed cleanly", because the whole reason yt-dlp declined is
	// that the submitters' duration disagrees with the real one — by 35s on
	// the Kinda Funny episode this was written for. Judging that case by the
	// tight tolerance filed it as Unclear and skipped the repair.
	if math.Abs(fileDuration-videoDuration) <= math.Max(tol, offered/2) {
		return NotApplied, 0
	}
	return Unclear, math.Max(0, videoDuration-fileDuration)
}

// Hints converts segments into spans for transcript confirmation.
func Hints(segs []Segment) []adtext.Span {
	out := make([]adtext.Span, 0, len(segs))
	for _, s := range segs {
		out = append(out, adtext.Span{Start: s.Start, End: s.End})
	}
	return out
}

// Describe renders segments for the event log.
func Describe(segs []Segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, fmt.Sprintf("%s %.0fs@%.0fs", s.Category, s.End-s.Start, s.Start))
	}
	return strings.Join(parts, "; ")
}
