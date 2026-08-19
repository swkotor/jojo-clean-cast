package autodl

import (
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
	"ikoyhn/podcast-sponsorblock/internal/services/downloader"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/playlist"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/labstack/gommon/log"
)

// tracks in-progress downloads (manual and automatic)
var inProgress = &sync.Map{}

// how far back a "new" episode can be published and still be auto-downloaded
const recentWindow = 7 * 24 * time.Hour

// how many of the most recent episodes per podcast are considered
const recentCount = 5

// IsDownloading reports whether a download for the video is in progress
func IsDownloading(videoId string) bool {
	_, ok := inProgress.Load(videoId)
	return ok
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
		events.Error("Download timed out after 2h: %s", name)
		return
	}
	if database.FileExistsWithId(audioDirAbs, videoId) {
		events.Info("Download finished: %s", name)
	} else {
		events.Error("Download failed (no file produced): %s — check container logs for yt-dlp output", name)
	}
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
		// initial check shortly after startup
		time.Sleep(30 * time.Second)
		runOnce()
		ticker := time.NewTicker(interval)
		for range ticker.C {
			runOnce()
		}
	}()
}

// CheckPodcast refreshes one podcast and downloads its recent episodes (async)
func CheckPodcast(podcastId string) {
	go func() {
		refreshPodcast(podcastId)
		downloadRecent(podcastId)
	}()
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
	for _, p := range podcasts {
		refreshPodcast(p.Id)
		downloadRecent(p.Id)
	}
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

func downloadRecent(podcastId string) {
	episodes, err := database.GetRecentEpisodes(podcastId, recentCount)
	if err != nil {
		events.Error("Auto-download: could not read episodes for %s: %v", podcastId, err)
		return
	}
	audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
	cutoff := time.Now().Add(-recentWindow)
	for _, ep := range episodes {
		if ep.PublishedDate.Before(cutoff) {
			continue
		}
		if database.FileExistsWithId(audioDirAbs, ep.YoutubeVideoId) || IsDownloading(ep.YoutubeVideoId) {
			continue
		}
		// sequential to avoid hammering YouTube
		Download(ep.YoutubeVideoId, ep.EpisodeName)
	}
}
