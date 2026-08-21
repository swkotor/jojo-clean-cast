package filterfeed

import (
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/enum"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
	"ikoyhn/podcast-sponsorblock/internal/services/playlist"
	"ikoyhn/podcast-sponsorblock/internal/services/rss"

	log "github.com/labstack/gommon/log"
)

// BuildFilteredRssFeed serves a virtual (title-filtered) sub-feed. It
// refreshes the parent podcast's episode list, then republishes only the
// episodes whose titles match the virtual feed's filter.
func BuildFilteredRssFeed(virtualId string, host string) []byte {
	v := database.GetPodcast(virtualId)
	if v == nil || !v.IsVirtual() {
		return nil
	}

	// refresh the parent's episodes (respects podcast-refresh-interval)
	parentType := database.GetEpisodeType(v.ParentId)
	if parentType == "CHANNEL" {
		channel.BuildChannelRssFeed(v.ParentId, &models.RssRequestParams{}, host)
	} else {
		playlist.BuildPlaylistRssFeed(v.ParentId, host)
	}

	episodes, err := database.GetEpisodesFiltered(v.ParentId, v.TitleFilter, v.ExcludeTerms())
	if err != nil {
		log.Error(err)
		return nil
	}

	// fall back to parent artwork if the virtual feed has none of its own
	if v.CustomImage == "" && v.ImageUrl == "" {
		if parent := database.GetPodcast(v.ParentId); parent != nil {
			v.ImageUrl = parent.ImageUrl
			if v.ArtistName == "" {
				v.ArtistName = parent.ArtistName
			}
		}
	}

	feedType := enum.PLAYLIST
	if parentType == "CHANNEL" {
		feedType = enum.CHANNEL
	}
	podcastRss := rss.BuildPodcast(*v, episodes)
	return rss.GenerateRssFeed(podcastRss, host, feedType)
}
