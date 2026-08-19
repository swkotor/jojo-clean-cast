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

// SetCustomName sets (or clears, with "") the user-defined feed name
func SetCustomName(podcastId, name string) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("custom_name", name).Error
}

// SetAutoDownload enables/disables automatic downloads for a podcast
func SetAutoDownload(podcastId string, enabled bool) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("auto_download_off", !enabled).Error
}

// SetCustomImage sets (or clears, with "") the custom cover art filename
func SetCustomImage(podcastId, filename string) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("custom_image", filename).Error
}

// SetSubscribed sets the user's manual "subscribed" marker
func SetSubscribed(podcastId string, subscribed bool) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("subscribed", subscribed).Error
}

// TouchFeedFetch records that a podcast feed was just fetched by a client
func TouchFeedFetch(podcastId string, ts int64) {
	db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("last_feed_fetch", ts)
}

// GetEpisodesBeyondRecent returns episodes after the N most recent ones
func GetEpisodesBeyondRecent(podcastId string, keep int) ([]models.PodcastEpisode, error) {
	var episodes []models.PodcastEpisode
	err := db.Where("podcast_id = ?", podcastId).
		Order("published_date DESC").
		Offset(keep).
		Find(&episodes).Error
	return episodes, err
}

// GetPlaybackHistory returns the playback history row for a video, or nil
func GetPlaybackHistory(videoId string) *models.EpisodePlaybackHistory {
	var h models.EpisodePlaybackHistory
	err := db.Where("youtube_video_id = ?", videoId).First(&h).Error
	if err != nil {
		return nil
	}
	return &h
}

// DeletePodcastAndEpisodes removes a podcast and all of its episode records
func DeletePodcastAndEpisodes(podcastId string) error {
	if err := db.Where("podcast_id = ?", podcastId).Delete(&models.PodcastEpisode{}).Error; err != nil {
		return err
	}
	return db.Where("id = ?", podcastId).Delete(&models.Podcast{}).Error
}
