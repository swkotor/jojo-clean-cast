package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
// them leaves the show. Two is the minimum that can detect anything; more
// reduces the chance an ad slot gets the same filler every time, at the cost of
// another full download.
func variantCount() int {
	if v := os.Getenv("AD_STRIP_VARIANTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 4 {
			return n
		}
	}
	return 2
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
		if err := fetchTo(ep.EnclosureUrl, p, variantAgents[i%len(variantAgents)]); err != nil {
			if i == 0 {
				return fmt.Errorf("download failed: %w", err)
			}
			// A later variant failing is not fatal; carry on with what we have.
			log.Warnf("[RSS] variant %d failed for %s: %v", i, ep.YoutubeVideoId, err)
			break
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no download succeeded")
	}

	out := filepath.Join(stagingDir, ep.YoutubeVideoId+".mp3")

	if len(paths) < 2 {
		return os.Rename(paths[0], out)
	}

	variants := make([][]adstrip.Frame, 0, len(paths))
	for _, p := range paths {
		f, err := adstrip.Parse(p)
		if err != nil || len(f) == 0 {
			log.Warnf("[RSS] %s: could not parse %s as MP3 — keeping the download as-is", ep.YoutubeVideoId, filepath.Base(p))
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

	if err := adstrip.Write(out, res.Frames); err != nil {
		return fmt.Errorf("writing stripped audio: %w", err)
	}
	if res.Removed >= 1 {
		events.Info("Removed %.0fs of ads from %s (%d slot(s))", res.Removed, ep.EpisodeName, len(res.Gaps))
	}
	log.Infof("[RSS] %s: %s", ep.YoutubeVideoId, res.String())
	return nil
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
