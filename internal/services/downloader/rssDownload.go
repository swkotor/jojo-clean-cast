package downloader

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/adstrip"
	"ikoyhn/podcast-sponsorblock/internal/services/common"
	"ikoyhn/podcast-sponsorblock/internal/services/events"

	log "github.com/labstack/gommon/log"
)

// Downloading an RSS episode more than once is the whole trick: hosts stitch
// ads in per request, so the copies differ only in the ads and intersecting
// them leaves the show.
//
// THREE, not two. Two is the minimum that can detect anything, but a host fills
// several ad slots per episode and any slot that happens to receive the same
// filler in both copies survives. Measured on Giant Bombcast 961: two copies
// found one slot (87s) and left a ~28s pre-roll in place, while three found
// three slots (145s) and removed it. The cost is one more full download.
func variantCount() int {
	if v := os.Getenv("AD_STRIP_VARIANTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 4 {
			return n
		}
	}
	return 3
}

// Each fetch presents a different client identity. The stitcher keys its ad
// decisions on the listener, so identical requests can be served the identical
// stitch from cache — which would leave nothing to compare.
var variantAgents = []string{
	"AppleCoreMedia/1.0.0.21G93 (iPhone; U; CPU OS 17_6 like Mac OS X)",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
	"Overcast/3.0 (+http://overcast.fm/; iOS podcast app)",
	"Pocket Casts/7.0 (Android)",
}

var rssClient = &http.Client{Timeout: 30 * time.Minute}

// downloadRssEpisode fetches an episode N times, removes whatever differs
// between the copies, and writes the result into the staging directory under
// the episode's key so the normal promote step publishes it.
//
// If only one variant is requested, or the comparison cannot be trusted, the
// first download is kept as-is: a listenable episode with ads beats no episode.
func downloadRssEpisode(ep *models.PodcastEpisode, stagingDir string) error {
	n := variantCount()
	paths := make([]string, 0, n)
	defer func() {
		for _, p := range paths {
			os.Remove(p)
		}
	}()

	for i := 0; i < n; i++ {
		p := filepath.Join(stagingDir, fmt.Sprintf("%s.v%d.part", ep.YoutubeVideoId, i))
		// Register the path BEFORE fetching. fetchTo creates the file
		// immediately and may have written most of a 170MB body before
		// failing; tracking it only on success left that partial behind
		// forever when the FIRST variant failed (nothing downstream sweeps
		// staging on that path).
		paths = append(paths, p)
		if err := fetchTo(ep.EnclosureUrl, p, variantAgents[i%len(variantAgents)]); err != nil {
			if i == 0 {
				return fmt.Errorf("download failed: %w", err)
			}
			// A later variant failing is not fatal; carry on with what we have.
			log.Warnf("[RSS] variant %d failed for %s: %v", i, ep.YoutubeVideoId, err)
			paths = paths[:len(paths)-1]
			os.Remove(p)
			break
		}
	}
	if len(paths) == 0 {
		return fmt.Errorf("no download succeeded")
	}

	out := filepath.Join(stagingDir, ep.YoutubeVideoId+enclosureExt(ep))

	if len(paths) < 2 {
		return os.Rename(paths[0], out)
	}

	variants := make([][]adstrip.Frame, 0, len(paths))
	for _, p := range paths {
		f, err := adstrip.Parse(p)
		// A non-MP3 enclosure (.m4a and .aac are common) still yields
		// thousands of false "frames", because any 0xFFEx byte pair looks like
		// a sync word. len(f) > 0 is therefore NOT enough: without a coverage
		// check the intersection of two AAC files produced a ~900KB file of
		// unrelated fragments, published as a 76-second episode of noise while
		// the real two-hour show was discarded.
		if err != nil || !framesCoverFile(p, f) {
			log.Warnf("[RSS] %s: %s is not usable MPEG audio — keeping the download as-is",
				ep.YoutubeVideoId, filepath.Base(p))
			return os.Rename(paths[0], out)
		}
		variants = append(variants, f)
	}

	res, err := adstrip.Intersect(variants)
	if err != nil {
		// Not the same episode twice, or nothing in common: publish variant 0
		// rather than a file spliced out of mismatched audio.
		log.Warnf("[RSS] %s: ad strip skipped (%v)", ep.YoutubeVideoId, err)
		events.Info("Ad removal skipped for %s: %v", ep.EpisodeName, err)
		return os.Rename(paths[0], out)
	}
	// A pathological alignment would throw most of the episode away. Treat a
	// big loss as a failed comparison, not as a very effective ad strip.
	if res.SharedPct < 50 {
		log.Warnf("[RSS] %s: only %.0f%% shared between downloads — keeping the original", ep.YoutubeVideoId, res.SharedPct)
		events.Error("Ad removal rejected for %s: downloads shared only %.0f%%", ep.EpisodeName, res.SharedPct)
		return os.Rename(paths[0], out)
	}
	// SharedPct is a ratio of the BASE file, so it cannot see a variant that is
	// simply shorter (a host re-encode, or a feed that swapped the file
	// mid-download). Those align over a prefix and the rest is silently
	// dropped — published as a successful strip with 40 minutes missing. Check
	// the result against what the feed says the episode should be, and against
	// how much any real ad load could plausibly be.
	if want := ep.Duration.Seconds(); want > 0 && res.Clean < 0.85*want {
		log.Warnf("[RSS] %s: result %.0fs is far short of the advertised %.0fs — keeping the original",
			ep.YoutubeVideoId, res.Clean, want)
		events.Error("Ad removal rejected for %s: result was %.0fs but the feed says %.0fs",
			ep.EpisodeName, res.Clean, want)
		return os.Rename(paths[0], out)
	}
	if res.Removed > 0.25*res.Original {
		log.Warnf("[RSS] %s: would cut %.0f%% of the episode — keeping the original",
			ep.YoutubeVideoId, 100*res.Removed/res.Original)
		events.Error("Ad removal rejected for %s: it would have cut %.0f%% of the episode",
			ep.EpisodeName, 100*res.Removed/res.Original)
		return os.Rename(paths[0], out)
	}

	if err := adstrip.Write(out, res.Frames); err != nil {
		return fmt.Errorf("writing stripped audio: %w", err)
	}
	if res.Removed >= 1 {
		events.Info("Removed %.0fs of ads from %s (%d slot(s))", res.Removed, ep.EpisodeName, len(res.Gaps))
	}
	log.Infof("[RSS] %s: %s", ep.YoutubeVideoId, res.String())
	return nil
}

// framesCoverFile reports whether the parsed frames account for essentially the
// whole file. Real MPEG audio is a contiguous run of frames, so coverage is
// ~100%; a file that merely contains occasional sync-looking bytes scores a few
// percent and must not be fed to the intersection.
func framesCoverFile(path string, frames []adstrip.Frame) bool {
	if len(frames) == 0 {
		return false
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return false
	}
	var covered int64
	for _, f := range frames {
		covered += int64(len(f.Data))
	}
	return float64(covered) >= 0.95*float64(st.Size())
}

// enclosureExt picks the published file's extension from the enclosure URL so
// the served Content-Type (derived from it) matches the actual bytes.
func enclosureExt(ep *models.PodcastEpisode) string {
	u, err := url.Parse(ep.EnclosureUrl)
	if err == nil {
		switch e := strings.ToLower(path.Ext(u.Path)); e {
		case ".mp3", ".m4a", ".mp4", ".aac", ".ogg", ".opus", ".flac", ".wav":
			return e
		}
	}
	return ".mp3"
}

func fetchTo(url, dest, agent string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", agent)
	// Ask for the whole file: a Range request can be served from a different
	// cache slice than a plain GET, which would break the comparison.
	req.Header.Set("Accept", "*/*")
	resp, err := rssClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return f.Sync()
}

// rssEpisodeFor returns the episode row when this key belongs to an RSS feed.
func rssEpisodeFor(episodeKey string) *models.PodcastEpisode {
	ep, err := database.GetEpisodeByVideoId(episodeKey)
	if err != nil || ep == nil || ep.EnclosureUrl == "" {
		return nil
	}
	return ep
}

// rssStorageDir mirrors the YouTube layout: one folder per podcast.
func rssStorageDir(ep *models.PodcastEpisode) string {
	dir := config.AppConfig.Setup.AudioDir
	if p := database.GetPodcast(ep.PodcastId); p != nil {
		if name := common.SanitizeDirName(p.DisplayName()); name != "" {
			dir = filepath.Join(dir, name)
		}
	}
	return dir
}

// runRssDownload is the RSS counterpart of the yt-dlp path: stage, strip,
// promote. It reuses promoteStagedFile so the atomic-rename guarantee (never
// let a client stream a half-written or mid-recut file) applies here too.
func runRssDownload(ep *models.PodcastEpisode) {
	title := ep.EpisodeName
	if title == "" {
		title = ep.YoutubeVideoId
	}
	downloadDir := rssStorageDir(ep)
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		events.Error("Could not create download dir for %s: %v", title, err)
		return
	}
	stagingDir := filepath.Join(downloadDir, ".incoming")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		events.Error("Could not create staging dir for %s: %v", title, err)
		return
	}

	events.Info("Downloading %s (%d copies, to compare away the ads)", title, variantCount())
	if err := downloadRssEpisode(ep, stagingDir); err != nil {
		events.Error("Download failed for %s: %v", title, err)
		log.Errorf("[RSS] %s: %v", ep.YoutubeVideoId, err)
		return
	}
	if err := promoteStagedFile(stagingDir, downloadDir, ep.YoutubeVideoId); err != nil {
		events.Error("Could not move finished download into place for %s: %v", title, err)
		return
	}
	// RSS audio has no SponsorBlock data; record a zero baseline so the drift
	// check never mistakes "no segments" for "the segments changed".
	database.SetSkipBaseline(ep.YoutubeVideoId, 0)
	events.Info("Download finished: %s", title)
}
