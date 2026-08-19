package autodl

import (
	"context"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
	"ikoyhn/podcast-sponsorblock/internal/services/downloader"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/playlist"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/labstack/gommon/log"
	"github.com/lrstanley/go-ytdlp"
)

// tracks in-progress downloads (manual and automatic)
var inProgress = &sync.Map{}

// tracks failed downloads for retry backoff: videoId -> failInfo
var failures = &sync.Map{}

type failInfo struct {
	Count int
	Last  time.Time
}

// how far back a "new" episode can be published and still be auto-downloaded
const recentWindow = 7 * 24 * time.Hour

// how many of the most recent episodes per feed are considered
const recentCount = 5

// IsDownloading reports whether a download for the video is in progress
func IsDownloading(videoId string) bool {
	_, ok := inProgress.Load(videoId)
	return ok
}

// FailureCount returns how many times a download has failed recently
func FailureCount(videoId string) int {
	if v, ok := failures.Load(videoId); ok {
		return v.(failInfo).Count
	}
	return 0
}

// backoffFor returns how long to wait after N failures before retrying
func backoffFor(count int) time.Duration {
	d := 10 * time.Minute
	for i := 1; i < count; i++ {
		d *= 2
		if d >= 6*time.Hour {
			return 6 * time.Hour
		}
	}
	return d
}

// inBackoff reports whether a video's failed download is still cooling down
func inBackoff(videoId string) bool {
	v, ok := failures.Load(videoId)
	if !ok {
		return false
	}
	f := v.(failInfo)
	return time.Since(f.Last) < backoffFor(f.Count)
}

// Download runs a download for one episode, tracking progress and logging events.
// Returns immediately if the file already exists or a download is in progress.
func Download(videoId, episodeName string) {
	if IsDownloading(videoId) {
		return
	}
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	if database.FileExistsWithId(audioDirAbs, videoId) {
		return
	}
	name := episodeName
	if name == "" {
		name = videoId
	}
	inProgress.Store(videoId, time.Now())
	defer inProgress.Delete(videoId)

	events.Info("Download started: %s", name)
	done := downloader.GetYoutubeVideo(videoId)
	select {
	case <-done:
	case <-time.After(2 * time.Hour):
		recordFailure(videoId)
		events.Error("Download timed out after 2h: %s", name)
		return
	}
	if database.FileExistsWithId(audioDirAbs, videoId) {
		failures.Delete(videoId)
		events.Info("Download finished: %s", name)
	} else {
		recordFailure(videoId)
		count := FailureCount(videoId)
		events.Error("Download failed (attempt %d, next retry in %s): %s",
			count, backoffFor(count).Round(time.Minute), name)
	}
}

func recordFailure(videoId string) {
	f := failInfo{Count: 1, Last: time.Now()}
	if v, ok := failures.Load(videoId); ok {
		f.Count = v.(failInfo).Count + 1
	}
	failures.Store(videoId, f)
}

// Start launches the background auto-download loop unless AUTO_DOWNLOAD=off
func Start() {
	if strings.EqualFold(os.Getenv("AUTO_DOWNLOAD"), "off") ||
		strings.EqualFold(os.Getenv("AUTO_DOWNLOAD"), "false") {
		log.Info("[AUTODL] Auto-download disabled via AUTO_DOWNLOAD env")
		return
	}
	interval := 30 * time.Minute
	if v := os.Getenv("AUTO_DOWNLOAD_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Minute {
			interval = d
		} else {
			events.Error("Invalid AUTO_DOWNLOAD_INTERVAL %q, using 30m", v)
		}
	}
	log.Infof("[AUTODL] Auto-download enabled, checking every %v", interval)
	events.Info("Auto-download enabled — checking for new episodes every %v", interval)

	go func() {
		// keep yt-dlp on the nightly channel — YouTube changes frequently
		// and stale versions cause HTTP 403 errors (ikoyhn/clean-cast#112)
		updateYtdlp()
		// initial check shortly after startup
		time.Sleep(30 * time.Second)
		runOnce()
		ticker := time.NewTicker(interval)
		lastUpdate := time.Now()
		for range ticker.C {
			if time.Since(lastUpdate) > 24*time.Hour {
				updateYtdlp()
				lastUpdate = time.Now()
			}
			runOnce()
		}
	}()
}

func updateYtdlp() {
	defer func() {
		if r := recover(); r != nil {
			events.Error("yt-dlp update crashed: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r, err := ytdlp.New().UpdateTo(ctx, "nightly")
	if err != nil {
		events.Error("yt-dlp self-update failed: %v", err)
		return
	}
	out := strings.TrimSpace(r.Stdout)
	if idx := strings.LastIndex(out, "\n"); idx >= 0 {
		out = out[idx+1:]
	}
	events.Info("yt-dlp update: %s", out)
	log.Infof("[AUTODL] %s", out)
}

// CheckPodcast refreshes one podcast and downloads its recent episodes (async)
func CheckPodcast(podcastId string) {
	go func() {
		refreshPodcast(podcastId)
		p := database.GetPodcast(podcastId)
		if p != nil && !p.AutoDownloadOff {
			downloadEpisodes(recentEpisodesFor(p))
		}
	}()
}

// recentEpisodesFor returns the feed's latest N episodes (filter-aware)
func recentEpisodesFor(p *models.Podcast) []models.PodcastEpisode {
	var eps []models.PodcastEpisode
	var err error
	if p.IsVirtual() {
		eps, err = database.GetRecentEpisodesFiltered(p.ParentId, p.TitleFilter, recentCount)
	} else {
		eps, err = database.GetRecentEpisodes(p.Id, recentCount)
	}
	if err != nil {
		events.Error("Could not read episodes for %s: %v", p.DisplayName(), err)
	}
	return eps
}

func runOnce() {
	defer func() {
		if r := recover(); r != nil {
			events.Error("Auto-download run crashed: %v", r)
			log.Errorf("[AUTODL] panic: %v", r)
		}
	}()

	podcasts, err := database.GetAllPodcasts()
	if err != nil {
		events.Error("Auto-download: could not list podcasts: %v", err)
		return
	}

	// group children under parents
	children := map[string][]models.Podcast{}
	for _, p := range podcasts {
		if p.IsVirtual() {
			children[p.ParentId] = append(children[p.ParentId], p)
		}
	}

	for i := range podcasts {
		p := podcasts[i]
		if p.IsVirtual() {
			continue // handled with parent
		}
		kids := children[p.Id]

		anyAuto := !p.AutoDownloadOff
		for _, k := range kids {
			if !k.AutoDownloadOff {
				anyAuto = true
			}
		}
		if anyAuto {
			refreshPodcast(p.Id)
		}
		if !p.AutoDownloadOff {
			downloadEpisodes(recentEpisodesFor(&p))
		}
		for i := range kids {
			if !kids[i].AutoDownloadOff {
				downloadEpisodes(recentEpisodesFor(&kids[i]))
			}
		}

		// keep set = latest N of parent + latest N of each child filter
		keep := map[string]bool{}
		for _, ep := range recentEpisodesFor(&p) {
			keep[ep.YoutubeVideoId] = true
		}
		for i := range kids {
			for _, ep := range recentEpisodesFor(&kids[i]) {
				keep[ep.YoutubeVideoId] = true
			}
		}
		cleanupServed(keep)
		pruneOldEpisodes(p.Id, keep)
	}

	enforceStorageCap()
}

// cleanupServed deletes local audio files for kept episodes that have
// already been served to a device (playback history exists) and were last
// accessed more than the cleanup window ago. The playback-history row is
// kept so the episode is not re-downloaded automatically.
func cleanupServed(keep map[string]bool) {
	window := 6 * time.Hour
	if v := os.Getenv("AUTO_CLEANUP_AFTER"); v != "" {
		if strings.EqualFold(v, "off") {
			return
		}
		if d, err := time.ParseDuration(v); err == nil && d >= time.Hour {
			window = d
		}
	}
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	for videoId := range keep {
		if IsDownloading(videoId) {
			continue
		}
		h := database.GetPlaybackHistory(videoId)
		if h == nil {
			continue // never served to a device yet
		}
		if time.Since(time.Unix(h.LastAccessDate, 0)) < window {
			continue
		}
		filePath := database.FindFileWithId(audioDirAbs, videoId)
		if filePath == "" {
			continue
		}
		if err := os.Remove(filePath); err == nil {
			name := videoId
			if ep, err := database.GetEpisodeByVideoId(videoId); err == nil && ep != nil {
				name = ep.EpisodeName
			}
			events.Info("Cleaned up served episode: %s (last accessed %s)",
				name, time.Unix(h.LastAccessDate, 0).Format("Jan 2 15:04"))
		}
	}
}

// pruneOldEpisodes deletes downloaded audio files for a podcast's episodes
// that are outside the keep set. Files modified in the last 24h are spared.
func pruneOldEpisodes(podcastId string, keep map[string]bool) {
	all, err := database.GetEpisodesBeyondRecent(podcastId, 0)
	if err != nil {
		return
	}
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	for _, ep := range all {
		if keep[ep.YoutubeVideoId] || IsDownloading(ep.YoutubeVideoId) {
			continue
		}
		filePath := database.FindFileWithId(audioDirAbs, ep.YoutubeVideoId)
		if filePath == "" {
			continue
		}
		if info, err := os.Stat(filePath); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
			continue // recently downloaded on purpose — keep for now
		}
		if err := os.Remove(filePath); err == nil {
			events.Info("Pruned old episode (outside latest %d): %s", recentCount, ep.EpisodeName)
		}
	}
}

// enforceStorageCap deletes oldest files when total storage exceeds
// MAX_STORAGE_GB (unset = no cap)
func enforceStorageCap() {
	capStr := os.Getenv("MAX_STORAGE_GB")
	if capStr == "" {
		return
	}
	capGb, err := strconv.ParseFloat(capStr, 64)
	if err != nil || capGb <= 0 {
		return
	}
	capBytes := int64(capGb * 1e9)

	type fileInfo struct {
		path string
		size int64
		mod  time.Time
	}
	var files []fileInfo
	var total int64
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	filepath.WalkDir(audioDirAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files = append(files, fileInfo{path, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= capBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= capBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			events.Info("Storage cap: removed oldest file %s (%.0f MB)",
				filepath.Base(f.path), float64(f.size)/1e6)
		}
	}
}

// StorageStats returns total files and bytes stored in the audio dir
func StorageStats() (int, int64) {
	var count int
	var total int64
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	filepath.WalkDir(audioDirAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			count++
			total += info.Size()
		}
		return nil
	})
	return count, total
}

// refreshPodcast pulls latest episode metadata from YouTube
// (respects the podcast-refresh-interval via LastBuildDate)
func refreshPodcast(podcastId string) {
	defer func() {
		if r := recover(); r != nil {
			events.Error("Refresh failed for %s: %v", podcastId, r)
		}
	}()
	podcastType := database.GetEpisodeType(podcastId)
	if podcastType == "CHANNEL" {
		channel.BuildChannelRssFeed(podcastId, &models.RssRequestParams{}, "http://localhost")
	} else {
		playlist.BuildPlaylistRssFeed(podcastId, "http://localhost")
	}
}

func downloadEpisodes(episodes []models.PodcastEpisode) {
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	cutoff := time.Now().Add(-recentWindow)
	for _, ep := range episodes {
		if ep.PublishedDate.Before(cutoff) {
			continue
		}
		if database.FileExistsWithId(audioDirAbs, ep.YoutubeVideoId) || IsDownloading(ep.YoutubeVideoId) {
			continue
		}
		if inBackoff(ep.YoutubeVideoId) {
			continue
		}
		// already served to a device before (file was cleaned up) — don't re-download
		if database.GetPlaybackHistory(ep.YoutubeVideoId) != nil {
			continue
		}
		// sequential to avoid hammering YouTube
		Download(ep.YoutubeVideoId, ep.EpisodeName)
	}
}
