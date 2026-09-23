package database

import (
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/enum"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"os"
	"path"
	"strings"
	"time"

	"github.com/labstack/gommon/log"
	"github.com/pkg/errors"
	ytApi "google.golang.org/api/youtube/v3"
	"gorm.io/gorm"
)

func SavePlaylistEpisodes(playlistEpisodes []models.PodcastEpisode) {
	db.CreateInBatches(playlistEpisodes, 100)
}

func EpisodeExists(youtubeVideoId string, episodeType string) (bool, error) {
	var episode models.PodcastEpisode
	err := db.Where("youtube_video_id = ? AND type = ?", youtubeVideoId, episodeType).First(&episode).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func GetLatestEpisode(podcastId string) (*models.PodcastEpisode, error) {
	var episode models.PodcastEpisode
	err := db.Where("podcast_id = ?", podcastId).Order("published_date DESC").First(&episode).Error
	if err != nil {
		return nil, err
	}
	return &episode, nil
}

func GetOldestEpisode(podcastId string) (*models.PodcastEpisode, error) {
	var episode models.PodcastEpisode
	err := db.Where("podcast_id = ?", podcastId).Order("published_date ASC").First(&episode).Error
	if err != nil {
		return nil, err
	}
	return &episode, nil
}

func GetAllPodcastEpisodeIds(podcastId string) ([]string, error) {
	var episodes []models.PodcastEpisode

	err := db.Where("podcast_id = ?", podcastId).Find(&episodes).Error
	if err != nil {
		return nil, err
	}

	var episodeIds []string
	for _, episode := range episodes {
		episodeIds = append(episodeIds, episode.YoutubeVideoId)
	}

	return episodeIds, nil
}

func IsEpisodeSaved(item *ytApi.Video) bool {
	exists, err := EpisodeExists(item.Id, "CHANNEL")
	if err != nil {
		log.Error(err)
	}
	if exists {
		return true
	}
	return false
}

func GetPodcastEpisodesByPodcastId(podcastId string, podcastType enum.PodcastType) ([]models.PodcastEpisode, error) {
	var episodes []models.PodcastEpisode
	if podcastType == enum.PLAYLIST {
		err := db.Where("podcast_id = ?", podcastId).
			Order("published_date DESC").
			Find(&episodes).Error
		if err != nil {
			return nil, err
		}
	} else if podcastType == enum.CHANNEL {
		dur := config.AppConfig.Ytdlp.EpisodeDurationMinimum

		err := db.Where("podcast_id = ? AND duration >= ?", podcastId, dur).
			Order("published_date DESC").
			Find(&episodes).Error
		if err != nil {
			return nil, err
		}
	}

	return episodes, nil
}

// DeletePodcastCronJob reclaims space from episodes a device fetched long ago.
//
// `isBusy` is injected rather than imported to avoid an import cycle (the
// download packages already depend on this one); it reports whether a download
// for that id is running right now.
func DeletePodcastCronJob(isBusy func(string) bool) {
	oneWeekAgo := time.Now().Add(-7 * 24 * time.Hour).Unix()

	var histories []models.EpisodePlaybackHistory
	// served only: an episode nobody has fetched must not be aged out here.
	// This used to match on last_access_date alone, which is also set when the
	// file is downloaded — so it deleted un-listened episodes, and by dropping
	// the row it also discarded the flag that stops them being re-fetched.
	db.Where("last_access_date < ? AND served = ?", oneWeekAgo, true).Find(&histories)

	for _, history := range histories {
		if isBusy != nil && isBusy(history.YoutubeVideoId) {
			continue
		}
		filePath := FindFileWithId(config.AppConfig.Setup.AudioDir, history.YoutubeVideoId)
		if filePath == "" {
			log.Debug("[DB] File not found when attempting to delete for video: " + history.YoutubeVideoId)
		} else {
			err := os.Remove(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					log.Debug("[DB] File not found when attempting to delete: " + filePath)
				} else {
					log.Warn("[DB] Failed to remove file: " + filePath + " error: " + err.Error())
					continue
				}
			}
		}

		// Keep the row. It carries `served`, which is what stops the auto
		// downloader immediately fetching the episode again; deleting it turned
		// this job into a weekly re-download loop.
		if upErr := db.Model(&history).Update("total_time_skipped", 0).Error; upErr != nil {
			log.Error("[DB] Failed to reset playback history for " + history.YoutubeVideoId + ": " + upErr.Error())
			continue
		}

		log.Info("[DB] Reclaimed space from old served episode... " + history.YoutubeVideoId)
	}
}

func GetEpisodeByVideoId(videoId string) (*models.PodcastEpisode, error) {
	var episode models.PodcastEpisode
	err := db.Where("youtube_video_id = ?", videoId).First(&episode).Error
	if err != nil {
		return nil, err
	}
	return &episode, nil
}

// FindFileWithId searches baseDir and its immediate subdirectories
// (per-podcast folders) for a file named <videoId>.<ext>
func FindFileWithId(baseDir, videoId string) string {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), videoId+".") {
			return path.Join(baseDir, entry.Name())
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subDir := path.Join(baseDir, entry.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() && strings.HasPrefix(sub.Name(), videoId+".") {
				return path.Join(subDir, sub.Name())
			}
		}
	}
	return ""
}

func FileExistsWithId(baseDir, videoId string) bool {
	return FindFileWithId(baseDir, videoId) != ""
}

// ListAudioFileNames returns the basenames of all audio files in baseDir
// and its immediate subdirectories
// isAudioFile keeps sidecars out of the episode list. Without this, a
// podcast folder's metadata.json and cover.jpg were tracked as if they were
// episodes, creating junk playback-history rows (and pointless SponsorBlock
// lookups for a "video" called "cover").
// IsAudioFileName reports whether a filename is an episode rather than a
// sidecar (cover art, metadata). Shared so cleanup passes never treat a
// podcast's artwork as something to delete for space.
func IsAudioFileName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".m4a", ".mp3", ".opus", ".ogg", ".aac", ".webm", ".mp4", ".flac", ".wav":
		return true
	}
	return false
}

func ListAudioFileNames(baseDir string) []string {
	var names []string
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if IsAudioFileName(entry.Name()) {
				names = append(names, entry.Name())
			}
			continue
		}
		subEntries, err := os.ReadDir(path.Join(baseDir, entry.Name()))
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() && IsAudioFileName(sub.Name()) {
				names = append(names, sub.Name())
			}
		}
	}
	return names
}
