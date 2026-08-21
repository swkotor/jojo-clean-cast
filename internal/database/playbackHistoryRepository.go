package database

import (
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/common"
	"os"
	"path"
	"strings"
	"time"

	"github.com/labstack/gommon/log"
)

func stripExtension(filename string) string {
	return strings.TrimSuffix(filename, path.Ext(filename))
}

func UpdateEpisodePlaybackHistory(youtubeVideoId string, totalTimeSkipped float64) {
	log.Info("[DB] Updating episode playback history...")
	var history models.EpisodePlaybackHistory
	db.Where(models.EpisodePlaybackHistory{YoutubeVideoId: youtubeVideoId}).
		Assign(map[string]interface{}{
			"last_access_date":   time.Now().Unix(),
			"total_time_skipped": totalTimeSkipped,
		}).
		FirstOrCreate(&history)
}

func GetEpisodePlaybackHistory(youtubeVideoId string) *models.EpisodePlaybackHistory {
	var history models.EpisodePlaybackHistory
	db.Where("youtube_video_id = ?", youtubeVideoId).First(&history)
	return &history
}

func TrackEpisodeFiles() {
	log.Info("App started, tracking existing episode files...")
	if _, err := os.Stat(config.AppConfig.Setup.AudioDir); os.IsNotExist(err) {
		os.MkdirAll(config.AppConfig.Setup.AudioDir, 0755)
	}
	if _, err := os.Stat(config.AppConfig.Setup.ConfigDir); os.IsNotExist(err) {
		os.MkdirAll(config.AppConfig.Setup.ConfigDir, 0755)
	}
	fileNames := ListAudioFileNames(config.AppConfig.Setup.AudioDir)

	dbFiles := make([]string, 0)
	db.Model(&models.EpisodePlaybackHistory{}).Pluck("YoutubeVideoId", &dbFiles)

	missingFiles := make([]string, 0)
	nonExistentDbFiles := make([]string, 0)
	for _, filename := range fileNames {
		if !common.IsValidFilename(filename) {
			continue
		}
		found := false
		for _, dbFile := range dbFiles {
			if dbFile == stripExtension(filename) {
				found = true
				break
			}
		}
		if !found {
			missingFiles = append(missingFiles, filename)
		}
	}

	for _, dbFile := range dbFiles {
		found := false
		for _, filename := range fileNames {
			if dbFile == stripExtension(filename) {
				found = true
				break
			}
		}
		if !found {
			nonExistentDbFiles = append(nonExistentDbFiles, dbFile)
		}
	}

	for _, filename := range missingFiles {
		id := stripExtension(filename)
		if !common.IsValidID(id) {
			continue
		}
		db.Create(&models.EpisodePlaybackHistory{YoutubeVideoId: id, LastAccessDate: time.Now().Unix(), TotalTimeSkipped: 0})
	}

	for _, dbFile := range nonExistentDbFiles {
		if !common.IsValidID(dbFile) {
			continue
		}
		db.Where("youtube_video_id = ?", dbFile).Delete(&models.EpisodePlaybackHistory{})
		log.Info("[DB] Deleted non-existent episode playback history... " + dbFile)
	}
}
