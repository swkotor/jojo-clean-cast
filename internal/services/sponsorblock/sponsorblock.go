package sponsorblock

import (
	"encoding/json"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/labstack/gommon/log"
)

const SPONSORBLOCK_API_URL = "https://sponsor.ajay.app/api/skipSegments?videoID="

var sponsorBlockClient = &http.Client{Timeout: 15 * time.Second}

// fork: episodes with no submitted segments 404 on every lookup, and the
// re-cut pass polls every tracked episode every half hour — which hammered
// the API and logged "Video not found" warnings around the clock. Remember
// misses for a while; segments appearing a few hours late only delays a
// re-cut, it never loses one.
var (
	sbMissMu    sync.Mutex
	sbMissUntil = map[string]time.Time{}
)

func DeterminePodcastDownload(youtubeVideoId string) (bool, float64) {
	episodeHistory := database.GetEpisodePlaybackHistory(youtubeVideoId)

	updatedSkippedTime, ok := lookupSkippedTime(youtubeVideoId)
	if episodeHistory == nil || episodeHistory.YoutubeVideoId == "" {
		return true, updatedSkippedTime
	}
	if !ok {
		// SponsorBlock could not be asked. Keep the stored baseline rather than
		// reading the failure as "all segments removed" and re-downloading.
		return false, episodeHistory.TotalTimeSkipped
	}

	if math.Abs(episodeHistory.TotalTimeSkipped-updatedSkippedTime) > 2 {
		log.Debug("[SponsorBlock] Updating downloaded episode with new sponsor skips...")
		return true, updatedSkippedTime
	}

	return false, updatedSkippedTime
}

// TotalSponsorTimeSkipped returns the seconds SponsorBlock offers for a video.
// It returns 0 when the lookup fails, so use lookupSkippedTime where a failure
// must not be mistaken for "no segments".
func TotalSponsorTimeSkipped(youtubeVideoId string) float64 {
	total, _ := lookupSkippedTime(youtubeVideoId)
	return total
}

// lookupSkippedTime also reports whether the answer is trustworthy. A network
// error, a rate limit or an unreadable response gives ok=false; a 404 ("no
// segments") and a cached miss are ok=true with 0.
func lookupSkippedTime(youtubeVideoId string) (float64, bool) {
	sbMissMu.Lock()
	until, missed := sbMissUntil[youtubeVideoId]
	sbMissMu.Unlock()
	if missed && time.Now().Before(until) {
		return 0, true
	}
	log.Debug("[SponsorBlock] Looking up podcast in SponsorBlock API...")
	endURL := SPONSORBLOCK_API_URL + youtubeVideoId

	if categories := getCategories(); categories != nil {
		for _, category := range categories {
			endURL += "&category=" + strings.TrimSpace(category)
		}
	}

	resp, err := sponsorBlockClient.Get(endURL)
	if err != nil {
		log.Error(err)
		return 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		sbMissMu.Lock()
		sbMissUntil[youtubeVideoId] = time.Now().Add(6 * time.Hour)
		sbMissMu.Unlock()
		log.Debugf("[SponsorBlock] no segments for %s (rechecking in 6h)", youtubeVideoId)
		return 0, true
	}

	body, bodyErr := io.ReadAll(resp.Body)
	if bodyErr != nil {
		log.Error(bodyErr)
		return 0, false
	}
	// A rate limit or server error has a non-JSON body, which fails to parse
	// here, so it is not mistaken for "no segments".
	sponsorBlockResponse, marshErr := unmarshalSponsorBlockResponse(body)
	if marshErr != nil {
		log.Error(marshErr)
		return 0, false
	}

	return calculateSkippedTime(sponsorBlockResponse), true
}

func unmarshalSponsorBlockResponse(data []byte) ([]SponsorBlockResponse, error) {
	var res []SponsorBlockResponse

	if err := json.Unmarshal(data, &res); err != nil {
		return []SponsorBlockResponse{}, err
	}

	return res, nil
}

// calculateSkippedTime returns the total seconds covered by the segments.
// The API returns segments ordered by votes, not by time, and submitters can
// overlap or nest them, so the spans are sorted and merged first. Summing them
// in API order double-counted overlaps and could go negative for nested ones.
func calculateSkippedTime(segments []SponsorBlockResponse) float64 {
	type span struct{ start, stop float64 }
	spans := make([]span, 0, len(segments))
	for _, segment := range segments {
		if len(segment.Segment) < 2 || segment.Segment[1] <= segment.Segment[0] {
			continue
		}
		spans = append(spans, span{segment.Segment[0], segment.Segment[1]})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	total := float64(0)
	open := false
	var curStart, curStop float64
	for _, s := range spans {
		if !open {
			curStart, curStop, open = s.start, s.stop, true
			continue
		}
		if s.start <= curStop {
			curStop = math.Max(curStop, s.stop)
			continue
		}
		total += curStop - curStart
		curStart, curStop = s.start, s.stop
	}
	if open {
		total += curStop - curStart
	}
	return total
}

func getCategories() []string {
	if config.AppConfig.Ytdlp.SponsorBlockCategories == "" {
		return nil
	}
	return strings.Split(config.AppConfig.Ytdlp.SponsorBlockCategories, ",")
}

type SponsorBlockResponse struct {
	Segment       []float64 `json:"segment"`
	UUID          string    `json:"UUID"`
	Category      string    `json:"category"`
	VideoDuration float64   `json:"videoDuration"`
	ActionType    string    `json:"actionType"`
	Locked        int16     `json:"locked"`
	Votes         int16     `json:"votes"`
	Description   string    `json:"description"`
}
