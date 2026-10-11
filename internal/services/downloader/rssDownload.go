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
	"ikoyhn/podcast-sponsorblock/internal/services/adtext"
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
	// Second pass, for ads the hosts read aloud. Runs AFTER promote so the
	// episode is already playable: if transcription fails or finds nothing, the
	// listener still has the de-added-by-comparison file.
	if final := database.FindFileWithId(config.AppConfig.Setup.AudioDir, ep.YoutubeVideoId); final != "" {
		stripHostReadAds(ep, database.GetPodcast(ep.PodcastId), final)
	}

	// RSS audio has no SponsorBlock data; record a zero baseline so the drift
	// check never mistakes "no segments" for "the segments changed".
	database.SetSkipBaseline(ep.YoutubeVideoId, 0)
	events.Info("Download finished: %s", title)
}

// stripHostReadAds is the second pass: it transcribes the finished episode and
// removes ads the presenters read aloud, which the differential comparison
// cannot see because they are identical in every copy.
//
// Deliberately conservative. It only ever runs when the podcast opts in, only
// on MPEG audio (where cutting is lossless and frame-exact), and it leaves the
// file untouched on any doubt — a missed ad is an annoyance, a wrong cut
// destroys part of the show.
// StripHostReadAdsFor runs the pass over an episode already on disk. Used to
// work through a backlog after a podcast opts in, without paying for another
// three downloads of a 145MB episode.
func StripHostReadAdsFor(videoId string) error {
	ep, err := database.GetEpisodeByVideoId(videoId)
	if err != nil || ep == nil {
		return fmt.Errorf("no such episode")
	}
	file := database.FindFileWithId(config.AppConfig.Setup.AudioDir, videoId)
	if file == "" {
		return fmt.Errorf("episode is not downloaded")
	}
	stripHostReadAds(ep, database.GetPodcast(ep.PodcastId), file)
	return nil
}

func stripHostReadAds(ep *models.PodcastEpisode, p *models.Podcast, file string) {
	if p == nil || !p.AdTextStrip {
		return
	}
	if ep.AdTextDone {
		return // already transcribed this file
	}
	cfg := config.AppConfig.Setup.ConfigDir
	if !adtext.Available(cfg) {
		if err := adtext.EnsureModel(cfg); err != nil {
			events.Error("Host-read ad removal unavailable: %v", err)
			return
		}
	}

	// MP3 is cut by dropping frames, which needs no external tool and is
	// byte-exact. Everything else (the YouTube feeds are AAC in MP4) is cut by
	// an ffmpeg stream copy, which rebuilds the container's sample tables
	// without decoding the audio.
	isMP3 := strings.EqualFold(filepath.Ext(file), ".mp3")

	var total float64
	var frames []adstrip.Frame
	if isMP3 {
		var err error
		frames, err = adstrip.Parse(file)
		if err != nil || len(frames) == 0 {
			log.Errorf("[ADTEXT] %s: could not parse frames", ep.YoutubeVideoId)
			return
		}
		total = adstrip.Duration(frames)
	} else {
		var err error
		if total, err = adtext.ProbeDuration(file); err != nil {
			log.Errorf("[ADTEXT] %s: %v", ep.YoutubeVideoId, err)
			return
		}
	}

	started := time.Now()
	segs, err := adtext.Transcribe(cfg, file, 8)
	if err != nil {
		events.Error("Could not transcribe %s: %v", ep.EpisodeName, err)
		return
	}
	spans := adtext.FindAds(segs, total, adtext.DefaultOptions())
	// Mark done either way: a clean episode must not be transcribed again on
	// every pass just because nothing was found.
	database.MarkAdTextDone(ep.YoutubeVideoId)
	if len(spans) == 0 {
		log.Infof("[ADTEXT] %s: %s (transcribed %.0fs in %s)",
			ep.YoutubeVideoId, adtext.Describe(spans), total, time.Since(started).Round(time.Second))
		return
	}

	var removed float64
	if isMP3 {
		keep := make([]adstrip.Frame, 0, len(frames))
		var at float64
		for _, f := range frames {
			inAd := false
			for _, sp := range spans {
				if at >= sp.Start && at < sp.End {
					inAd = true
					break
				}
			}
			if inAd {
				removed += f.Seconds
			} else {
				keep = append(keep, f)
			}
			at += f.Seconds
		}
		if removed <= 0 {
			return
		}
		tmp := file + ".adtext"
		if err := adstrip.Write(tmp, keep); err != nil {
			os.Remove(tmp)
			events.Error("Could not write de-added audio for %s: %v", ep.EpisodeName, err)
			return
		}
		if err := os.Rename(tmp, file); err != nil {
			os.Remove(tmp)
			events.Error("Could not replace audio for %s: %v", ep.EpisodeName, err)
			return
		}
	} else {
		if removed, err = adtext.CutStreamCopy(file, spans, total); err != nil {
			events.Error("Could not remove host-read ads from %s: %v", ep.EpisodeName, err)
			return
		}
	}

	// the episode is shorter now, so the advertised duration must follow or
	// clients show a progress bar that runs past the end
	if d, err := adtext.ProbeDuration(file); err == nil && d > 0 {
		database.UpdateEpisodeDuration(ep.YoutubeVideoId, int(d))
	}
	events.Info("Removed %.0fs of host-read ads from %s (%s)",
		removed, ep.EpisodeName, adtext.Describe(spans))
	log.Infof("[ADTEXT] %s: cut %.0fs in %d span(s) — %s", ep.YoutubeVideoId,
		removed, len(spans), adtext.Describe(spans))
}
