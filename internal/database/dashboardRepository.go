package database

import (
	"ikoyhn/podcast-sponsorblock/internal/models"

	"gorm.io/gorm"
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

// filteredQuery builds a query for a podcast's episodes with an optional
// include filter and any number of exclude terms (all case-insensitive
// substring matches on the episode title)
func filteredQuery(podcastId, filter string, exclude []string) *gorm.DB {
	query := db.Model(&models.PodcastEpisode{}).Where("podcast_id = ?", podcastId)
	if filter != "" {
		query = query.Where("episode_name LIKE ?", "%"+filter+"%")
	}
	for _, term := range exclude {
		query = query.Where("episode_name NOT LIKE ?", "%"+term+"%")
	}
	return query
}

// GetRecentEpisodesFiltered returns the most recent N episodes whose titles
// match the filter and avoid the exclude terms
func GetRecentEpisodesFiltered(podcastId, filter string, exclude []string, limit int) ([]models.PodcastEpisode, error) {
	var episodes []models.PodcastEpisode
	err := filteredQuery(podcastId, filter, exclude).
		Order("published_date DESC").
		Limit(limit).
		Find(&episodes).Error
	return episodes, err
}

// GetEpisodesFiltered returns all matching episodes, newest first
func GetEpisodesFiltered(podcastId, filter string, exclude []string) ([]models.PodcastEpisode, error) {
	var episodes []models.PodcastEpisode
	err := filteredQuery(podcastId, filter, exclude).
		Order("published_date DESC").
		Find(&episodes).Error
	return episodes, err
}

// SearchEpisodes returns a page of episodes for the browser, with optional
// title search plus the feed's filter/excludes, and the total match count
func SearchEpisodes(podcastId, filter string, exclude []string, q string, offset, limit int) ([]models.PodcastEpisode, int64, error) {
	query := filteredQuery(podcastId, filter, exclude)
	if q != "" {
		query = query.Where("episode_name LIKE ?", "%"+q+"%")
	}
	var total int64
	query.Count(&total)
	var episodes []models.PodcastEpisode
	err := query.Order("published_date DESC").Offset(offset).Limit(limit).Find(&episodes).Error
	return episodes, total, err
}

// GetChildFeeds returns the filtered sub-feeds of a podcast
func GetChildFeeds(parentId string) []models.Podcast {
	var podcasts []models.Podcast
	db.Where("parent_id = ?", parentId).Find(&podcasts)
	return podcasts
}

// CountEpisodesFiltered counts episodes matching a filter and excludes
func CountEpisodesFiltered(podcastId, filter string, exclude []string) int64 {
	var count int64
	filteredQuery(podcastId, filter, exclude).Count(&count)
	return count
}

// SetChannelInfo records the owning channel of a podcast for UI grouping
func SetChannelInfo(podcastId, channelId, title, thumb, banner string) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Updates(map[string]interface{}{
			"channel_id":     channelId,
			"channel_title":  title,
			"channel_thumb":  thumb,
			"channel_banner": banner,
		}).Error
}

// UpdatePodcastNameAndImage corrects a feed's YouTube-derived name (and
// artwork, if the feed has no custom cover)
func UpdatePodcastNameAndImage(podcastId, name, imageUrl string) error {
	updates := map[string]interface{}{"podcast_name": name}
	if imageUrl != "" {
		var p models.Podcast
		if err := db.Where("id = ?", podcastId).First(&p).Error; err == nil && p.CustomImage == "" {
			updates["image_url"] = imageUrl
		}
	}
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).Updates(updates).Error
}

// GetPodcastsByChannel returns all podcasts belonging to a channel
func GetPodcastsByChannel(channelId string) []models.Podcast {
	var podcasts []models.Podcast
	db.Where("channel_id = ?", channelId).Find(&podcasts)
	return podcasts
}

// SetSponsorblockCategories sets the per-podcast SponsorBlock override
func SetSponsorblockCategories(podcastId, categories string) error {
	return db.Model(&models.Podcast{}).Where("id = ?", podcastId).
		Update("sponsorblock_categories", categories).Error
}

// TotalTimeSkipped sums the SponsorBlock time skipped across all history
func TotalTimeSkipped() float64 {
	var total float64
	db.Model(&models.EpisodePlaybackHistory{}).
		Select("COALESCE(SUM(total_time_skipped), 0)").Scan(&total)
	return total
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
