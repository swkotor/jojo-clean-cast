package database

import (
	"ikoyhn/podcast-sponsorblock/internal/models"
)

// GetAllPodcasts returns all subscribed podcasts ordered by name
func GetAllPodcasts() ([]models.Podcast, error) {
	var podcasts []models.Podcast
	err := db.Order("podcast_name ASC").Find(&podcasts).Error
	if err != nil {
		return nil, err
	}
	return podcasts, nil
}

// GetRecentEpisodes returns the most recent N episodes for a podcast
func GetRecentEpisodes(podcastId string, limit int) ([]models.PodcastEpisode, error) {
	var episodes []models.PodcastEpisode
	err := db.Where("podcast_id = ?", podcastId).
		Order("published_date DESC").
		Limit(limit).
		Find(&episodes).Error
	if err != nil {
		return nil, err
	}
	return episodes, nil
}

// CountEpisodes returns the total number of tracked episodes for a podcast
func CountEpisodes(podcastId string) int64 {
	var count int64
	db.Model(&models.PodcastEpisode{}).Where("podcast_id = ?", podcastId).Count(&count)
	return count
}

// GetEpisodeType returns the type (PLAYLIST/CHANNEL) of a podcast's episodes
func GetEpisodeType(podcastId string) string {
	var episode models.PodcastEpisode
	err := db.Where("podcast_id = ?", podcastId).First(&episode).Error
	if err != nil {
		return ""
	}
	return episode.Type
}

// DeletePodcastAndEpisodes removes a podcast and all of its episode records
func DeletePodcastAndEpisodes(podcastId string) error {
	if err := db.Where("podcast_id = ?", podcastId).Delete(&models.PodcastEpisode{}).Error; err != nil {
		return err
	}
	return db.Where("id = ?", podcastId).Delete(&models.Podcast{}).Error
}
