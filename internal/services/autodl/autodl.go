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
	"ikoyhn/podcast-sponsorblock/internal/services/rssfeed"
	"ikoyhn/podcast-sponsorblock/internal/services/sponsorblock"
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
	// Ask the downloader too. Our own marker is cleared when we STOP WAITING,
	// which is not the same as the download stopping - on the watchdog path the
	// file is still being written, and treating it as idle let the cleanup
	// passes delete it mid-write.
	if downloader.IsActive(videoId) {
		return true
	}
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
	if downloader.IsActive(videoId) {
		return
	}
	// LoadOrStore, not Load-then-Store: the dashboard starts this in a
	// goroutine per button press, so two quick clicks (or a click racing the
	// poller) could both pass a plain check and both run.
	if _, loaded := inProgress.LoadOrStore(videoId, time.Now()); loaded {
		return
	}
	defer inProgress.Delete(videoId)
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	if database.FileExistsWithId(audioDirAbs, videoId) {
		return
	}
	name := episodeName
	if name == "" {
		name = videoId
	}
	events.Info("Download started: %s", name)
	done := downloader.GetYoutubeVideo(videoId, false)
	select {
	case <-done:
	case <-time.After(2*time.Hour + time.Minute):
		// The downloader has its own, shorter deadline; if we get here it is
		// already unwinding. Leave the claim to downloader.IsActive rather than
		// declaring the episode idle.
		recordFailure(videoId)
		events.Error("Download timed out: %s", name)
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

// NoteFailure records a failed download attempt from outside this package
// (the on-demand /media path) so retry backoff and the dashboard failure
// badge apply to those failures too.
func NoteFailure(videoId string) {
	recordFailure(videoId)
}

// failMu guards the read-modify-write below. sync.Map has no atomic update, so
// concurrent failures for the same video silently lost increments and the
// backoff never grew past its first step.
var failMu sync.Mutex

func recordFailure(videoId string) {
	failMu.Lock()
	defer failMu.Unlock()
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
	// fork: log BEFORE running. The app died silently (exit 0, nothing in the
	// logs) every night at exactly the daily-update tick for weeks; if that
	// ever happens again, this marker pinpoints it. The updater also runs in
	// its own process group so it cannot signal PID 1.
	log.Info("[AUTODL] running yt-dlp self-update...")
	r, err := ytdlp.New().SetSeparateProcessGroup(true).UpdateTo(ctx, "nightly")
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
		eps, err = database.GetRecentEpisodesFiltered(p.ParentId, p.TitleFilter, p.ExcludeTerms(), recentCount)
	} else {
		eps, err = database.GetRecentEpisodes(p.Id, recentCount)
	}
	if err != nil {
		events.Error("Could not read episodes for %s: %v", p.DisplayName(), err)
		// Return nil, not a partial list: callers delete everything NOT in the
		// keep set, so one transient SQLite error would wipe a podcast's whole
		// back catalogue.
		return nil
	}
	return eps
}

// How long an episode must have gone untouched before we are willing to
// replace its file. A podcast client pulls a long episode over many range
// requests spread over minutes; swapping the file underneath it splices in
// audio from a different cut of the show. Idle-gating is what makes a re-cut
// safe, so this wants to be comfortably longer than a slow client's download.
const recutIdlePeriod = 45 * time.Minute

// refreshChangedCuts re-downloads episodes whose SponsorBlock segments have
// changed since the file was written. This used to happen inline on the media
// endpoint, which is exactly what corrupted in-flight downloads; here it can
// only touch episodes nobody has asked for recently.
func refreshChangedCuts() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[AUTODL] re-cut pass panic: %v", r)
		}
	}()

	audioDirAbs, err := filepath.Abs(config.AppConfig.Setup.AudioDir)
	if err != nil {
		return
	}
	episodes, err := database.GetAllPlaybackHistory()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-recutIdlePeriod).Unix()
	for _, h := range episodes {
		if h.YoutubeVideoId == "" || h.LastAccessDate > cutoff {
			continue // recently served — a client may still be fetching it
		}
		if IsDownloading(h.YoutubeVideoId) || inBackoff(h.YoutubeVideoId) {
			continue
		}
		if !database.FileExistsWithId(audioDirAbs, h.YoutubeVideoId) {
			continue
		}
		current := sponsorblock.TotalSponsorTimeSkipped(h.YoutubeVideoId)
		// A miss (0) is ambiguous — SponsorBlock being unreachable looks the
		// same as "no segments". Only act when there is something to cut, so a
		// flaky lookup can never wipe out an episode's existing cuts.
		if current <= 0 || absDiff(current, h.TotalTimeSkipped) <= 2 {
			continue
		}
		events.Info("SponsorBlock segments changed, re-cutting %s (%.0fs -> %.0fs)",
			h.YoutubeVideoId, h.TotalTimeSkipped, current)
		inProgress.Store(h.YoutubeVideoId, time.Now())
		// defer inside the loop body via a func: a panic between here and the
		// Delete below used to leave the entry in place for the life of the
		// process, permanently marking the episode as downloading.
		defer inProgress.Delete(h.YoutubeVideoId)
		select {
		case <-downloader.GetYoutubeVideo(h.YoutubeVideoId, true):
		case <-time.After(2 * time.Hour):
			events.Error("Re-cut timed out: %s", h.YoutubeVideoId)
		}
		inProgress.Delete(h.YoutubeVideoId)
		return // one per pass — re-cuts are maintenance, not a priority
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
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

	defer refreshChangedCuts()

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
		if h == nil || !h.Served {
			continue // never fetched by a device yet — keep it ready to play
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
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Never descend into staging: those files belong to a download still
			// running, and removing one leaves yt-dlp writing to a deleted inode
			// and the promote step finding nothing.
			if d.Name() == ".incoming" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only episodes count towards the cap, and only episodes are freed to
		// meet it. metadata.json and cover.jpg are tiny and are what keeps a
		// podcast describable — they were deleted first simply for being oldest.
		if !database.IsAudioFileName(d.Name()) {
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
		if IsDownloading(strings.TrimSuffix(filepath.Base(f.path), filepath.Ext(f.path))) {
			continue
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
	// An RSS-sourced podcast is refreshed from its feed, not from the YouTube
	// API. Source() defaults to youtube, so nothing else changes.
	if p := database.GetPodcast(podcastId); p != nil && p.IsRss() {
		if err := rssfeed.Refresh(p); err != nil {
			events.Error("Feed refresh failed for %s: %v", p.DisplayName(), err)
		}
		return
	}
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
		// Already fetched by a device and since cleaned up — don't re-download.
		// Gated on `served`, not on the row existing: the row is also created to
		// record what SponsorBlock cut, and reading that as "served" stopped
		// every episode from ever being pre-downloaded again.
		if h := database.GetPlaybackHistory(ep.YoutubeVideoId); h != nil && h.Served {
			continue
		}
		// sequential to avoid hammering YouTube
		Download(ep.YoutubeVideoId, ep.EpisodeName)
	}
}
