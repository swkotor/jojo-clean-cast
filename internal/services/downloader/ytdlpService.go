package downloader

import (
	"context"
	"fmt"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/services/common"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/ntfy"
	"ikoyhn/podcast-sponsorblock/internal/services/sponsorblock"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/labstack/gommon/log"
	"github.com/lrstanley/go-ytdlp"
)

// Per-video serialisation. Refcounted so the map does not grow forever: with a
// bare sync.Map every video id ever requested (including bad ones hitting
// /media) left a mutex behind for the life of the process.
type videoLock struct {
	mu   sync.Mutex
	refs int
}

var (
	lockMapMu  sync.Mutex
	videoLocks = map[string]*videoLock{}
	// Downloads currently running, owned BY THE DOWNLOADER. Callers used to
	// track this themselves and clear it when they gave up waiting, which made
	// an episode look idle while yt-dlp was still writing it - long enough for
	// the cleanup passes to delete the file mid-write.
	activeDownloads sync.Map
)

func acquireVideoLock(id string) *videoLock {
	lockMapMu.Lock()
	l := videoLocks[id]
	if l == nil {
		l = &videoLock{}
		videoLocks[id] = l
	}
	l.refs++
	lockMapMu.Unlock()
	l.mu.Lock()
	return l
}

func releaseVideoLock(id string, l *videoLock) {
	l.mu.Unlock()
	lockMapMu.Lock()
	l.refs--
	if l.refs == 0 {
		delete(videoLocks, id)
	}
	lockMapMu.Unlock()
}

// IsActive reports whether a download for this video is running right now.
func IsActive(youtubeVideoId string) bool {
	_, ok := activeDownloads.Load(youtubeVideoId)
	return ok
}

// downloadTimeout bounds a single yt-dlp run. Without it a wedged child process
// held the per-video lock forever, and because callers blocked acquiring that
// lock their own watchdogs never started - one stuck download froze the whole
// auto-download poller until the container was restarted.
const downloadTimeout = 2 * time.Hour

const youtubeVideoUrl = "https://www.youtube.com/watch?v="

// GetYoutubeVideo starts a download and returns a channel closed when it
// finishes. It returns IMMEDIATELY: the per-video lock is taken inside the
// goroutine, so a caller's select/timeout is armed before any waiting begins.
// Taking the lock in the caller's goroutine meant a caller could block for
// hours before its own watchdog was even running.
func GetYoutubeVideo(youtubeVideoId string, forceRedownload bool) <-chan struct{} {
	done := make(chan struct{})
	go runDownload(youtubeVideoId, forceRedownload, done)
	return done
}

func runDownload(youtubeVideoId string, forceRedownload bool, done chan struct{}) {
	defer close(done)
	defer func() {
		if rec := recover(); rec != nil {
			log.Errorf("Panic while downloading %s: %v", youtubeVideoId, rec)
		}
	}()

	lock := acquireVideoLock(youtubeVideoId)
	defer releaseVideoLock(youtubeVideoId, lock)

	if !forceRedownload && database.FileExistsWithId(config.AppConfig.Setup.AudioDir, youtubeVideoId) {
		return
	}
	activeDownloads.Store(youtubeVideoId, time.Now())
	defer activeDownloads.Delete(youtubeVideoId)

	// RSS-sourced episodes are plain audio files from a podcast host, not
	// YouTube videos: they are fetched over HTTP several times and de-added by
	// comparing the copies. Branching here keeps every caller — the media
	// route, the poller, the dashboard button — unchanged.
	if ep := rssEpisodeFor(youtubeVideoId); ep != nil {
		runRssDownload(ep)
		return
	}

	title := youtubeVideoId
	episode, err := database.GetEpisodeByVideoId(youtubeVideoId)
	if err != nil {
		log.Warnf("Error fetching YouTube video title: %v", err)
	}
	if episode != nil && episode.EpisodeName != "" {
		title = episode.EpisodeName
	}

	// store episodes in a per-podcast folder named after the podcast, and
	// honor a per-podcast SponsorBlock category override if one is set
	downloadDir := config.AppConfig.Setup.AudioDir
	sbOverride := ""
	if episode != nil && episode.PodcastId != "" {
		if p := database.GetPodcast(episode.PodcastId); p != nil {
			if dirName := common.SanitizeDirName(p.DisplayName()); dirName != "" {
				downloadDir = filepath.Join(downloadDir, dirName)
			}
			sbOverride = p.SponsorblockCategories
		}
	}
	if mkErr := os.MkdirAll(downloadDir, 0o755); mkErr != nil {
		log.Errorf("Could not create download dir %s: %v", downloadDir, mkErr)
		downloadDir = config.AppConfig.Setup.AudioDir
	}

	// Download into a staging directory on the same filesystem, then move the
	// finished file into place with an atomic rename. Writing directly to the
	// live file lets a podcast client stream a half-written (or re-cut) file,
	// which produces audible splices — audio from another part of the show.
	stagingDir := filepath.Join(downloadDir, ".incoming")
	if mkErr := os.MkdirAll(stagingDir, 0o755); mkErr != nil {
		log.Errorf("Could not create staging dir %s: %v", stagingDir, mkErr)
		stagingDir = downloadDir
	}

	categories := config.AppConfig.Ytdlp.SponsorBlockCategories
	if sbOverride != "" {
		categories = sbOverride
	}
	categories = strings.TrimSpace(categories)

	var etaNotified uint32 = 0
	dl := ytdlp.New().
		// fork: yt-dlp needs a JS runtime for YouTube's player challenges;
		// node is installed in the image (deno has no musl build). Without
		// it the deprecated no-JS path intermittently gets 403 Forbidden.
		JsRuntimes("node").
		// fork: keep yt-dlp/ffmpeg in their own process group so nothing
		// they do with signals can reach PID 1.
		SetSeparateProcessGroup(true).
		NoProgress().
		Format("bestaudio[ext=m4a]/bestaudio[ext=aac]/bestaudio[ext=opus]/bestaudio[ext=vorbis]/bestaudio/best").
		SponsorblockRemove(categories).
		RemoteComponents("ejs:github").
		ExtractAudio().
		NoPlaylist().
		FFmpegLocation("/usr/bin/ffmpeg").
		Continue().
		Paths(stagingDir).
		ProgressFunc(4000*time.Millisecond, func(prog ytdlp.ProgressUpdate) {
			ytdlpProgress(&etaNotified, prog, title)
		}).
		Output(youtubeVideoId + ".%(ext)s")

	if forceRedownload {
		dl.ForceOverwrites()
	}
	if config.AppConfig.Ytdlp.CookiesFile != "" {
		dl.Cookies(config.AppConfig.Ytdlp.CookiesFile)
	}
	if config.AppConfig.Ytdlp.YtdlpExtractorArgs != "" {
		dl.ExtractorArgs(config.AppConfig.Ytdlp.YtdlpExtractorArgs)
	}

	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	r, dlErr := dl.Run(ctx, youtubeVideoUrl+youtubeVideoId)
	{

		if r == nil {
			promoteStagedFile(stagingDir, downloadDir, youtubeVideoId)
			if database.FileExistsWithId(config.AppConfig.Setup.AudioDir, youtubeVideoId) {
				database.SetSkipBaseline(youtubeVideoId, sponsorblock.TotalSponsorTimeSkipped(youtubeVideoId))
				ntfy.SendNotification("Download completed!", "Clean Cast - Success")
				log.Warn("Download returned no result, but file exists: ", youtubeVideoId)
			} else {
				ntfy.SendNotification("Download failed!", "Clean Cast - Error")
				log.Errorf("Error downloading YouTube video %s: %v", youtubeVideoId, dlErr)
			}
			return
		}

		if r.ExitCode != 0 {
			promoteStagedFile(stagingDir, downloadDir, youtubeVideoId)
			if database.FileExistsWithId(config.AppConfig.Setup.AudioDir, youtubeVideoId) {
				database.SetSkipBaseline(youtubeVideoId, sponsorblock.TotalSponsorTimeSkipped(youtubeVideoId))
				ntfy.SendNotification("Download completed!", "Clean Cast - Success")
				log.Warn("Download exited with non-zero code, but file exists: ", youtubeVideoId)
			} else {
				if dlErr != nil {
					ntfy.SendNotification("Download failed!", "Clean Cast - Error")
					log.Errorf("Error downloading YouTube video: %v", dlErr)
					events.Error("yt-dlp failed for %s (%s): %v", title, youtubeVideoId, dlErr)
				} else {
					events.Error("yt-dlp exited with code %d for %s (%s)", r.ExitCode, title, youtubeVideoId)
				}
			}
		} else {
			if err := promoteStagedFile(stagingDir, downloadDir, youtubeVideoId); err != nil {
				events.Error("Could not move finished download into place for %s: %v", title, err)
				log.Errorf("[DOWNLOAD] promote failed for %s: %v", youtubeVideoId, err)
				return
			}
			// Record what SponsorBlock removed from THIS file, so a later
			// serve doesn't mistake "no baseline" for "the segments changed"
			// and re-download the episode out from under a streaming client.
			database.SetSkipBaseline(youtubeVideoId, sponsorblock.TotalSponsorTimeSkipped(youtubeVideoId))
			log.Infof("%s download completed successfully.", title)
			ntfy.SendNotification(fmt.Sprintf("%s download success!", title), "Clean Cast - Success")
		}
	}
}

func ytdlpProgress(etaNotified *uint32, prog ytdlp.ProgressUpdate, title string) {
	fmt.Printf(
		"%s @ %s [eta: %s] :: %s\n",
		prog.Status,
		prog.PercentString(),
		prog.ETA(),
		prog.Filename,
	)

	if atomic.LoadUint32(etaNotified) == 0 {
		eta := prog.ETA()
		if eta > time.Duration(0) {
			if atomic.CompareAndSwapUint32(etaNotified, 0, 1) {
				msg := fmt.Sprintf("%s — %s @ %s (eta: %s)", title, prog.Status, prog.PercentString(), eta)
				ntfy.SendNotification(msg, "Clean Cast")
			}
		}
	}
}

// promoteStagedFile atomically moves a finished download from the staging
// directory into the podcast's folder, replacing any previous version.
func promoteStagedFile(stagingDir, downloadDir, youtubeVideoId string) error {
	if stagingDir == downloadDir {
		return nil
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return err
	}
	moved := false
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, youtubeVideoId+".") {
			continue
		}
		// ignore yt-dlp working files
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") ||
			strings.Contains(name, ".part-") || strings.HasSuffix(name, ".temp") {
			continue
		}
		src := filepath.Join(stagingDir, name)
		dst := filepath.Join(downloadDir, name)
		if err := os.Rename(src, dst); err != nil {
			return err
		}
		moved = true
	}
	if !moved {
		return nil
	}
	// clean up any leftovers for this video in staging
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), youtubeVideoId+".") {
			os.Remove(filepath.Join(stagingDir, entry.Name()))
		}
	}
	return nil
}
