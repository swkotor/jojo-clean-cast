package rssfeed

import (
	"fmt"
	"strings"
	"time"

	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"

	log "github.com/labstack/gommon/log"
)

// Ingest resolves whatever the user pasted to a feed, fetches it, and creates
// or updates the podcast and its episodes. Returns the stored podcast.
//
// Episodes are keyed by a synthetic "rss_<hash-of-guid>" in YoutubeVideoId, so
// every existing code path — media URLs, file names, playback history, cleanup —
// treats them exactly like a YouTube episode without special-casing.
func Ingest(rawURL string) (*models.Podcast, error) {
	feedURL, err := Resolve(rawURL)
	if err != nil {
		return nil, err
	}
	id := PodcastKey(feedURL)

	f, err := Fetch(feedURL, id)
	if err != nil {
		return nil, err
	}
	if f.Title == "" {
		return nil, fmt.Errorf("that feed has no title — it may not be a podcast feed")
	}

	existing := database.GetPodcast(id)
	p := &models.Podcast{
		Id:            id,
		PodcastName:   f.Title,
		Description:   f.Description,
		ArtistName:    f.Author,
		ImageUrl:      f.ImageURL,
		Category:      f.Category,
		Explicit:      f.Explicit,
		SourceType:    "rss",
		FeedUrl:       feedURL,
		LastFeedFetch: time.Now().Unix(),
	}
	if existing != nil {
		// Preserve everything the user has set on this podcast; only the
		// feed-derived fields are refreshed.
		p.CustomName = existing.CustomName
		p.CustomImage = existing.CustomImage
		p.AutoImage = existing.AutoImage
		p.AutoDownloadOff = existing.AutoDownloadOff
		p.Subscribed = existing.Subscribed
		p.SponsorblockCategories = existing.SponsorblockCategories
		database.UpdatePodcast(p)
	} else {
		database.SavePodcast(p)
	}

	if n := saveEpisodes(id, f.Episodes); n > 0 {
		log.Infof("[RSS] %s: %d new episode(s)", f.Title, n)
	}
	return database.GetPodcast(id), nil
}

// Refresh re-reads an already-added feed. Used by the auto-download poller.
func Refresh(p *models.Podcast) error {
	if p == nil || strings.TrimSpace(p.FeedUrl) == "" {
		return fmt.Errorf("podcast has no feed url")
	}
	f, err := Fetch(p.FeedUrl, p.Id)
	if err != nil {
		return err
	}
	saveEpisodes(p.Id, f.Episodes)
	p.LastFeedFetch = time.Now().Unix()
	database.UpdatePodcast(p)
	return nil
}

// saveEpisodes inserts episodes we have not seen before and returns how many.
// Existing rows are left alone so a feed edit cannot rewrite an episode the
// user already has on disk.
func saveEpisodes(podcastID string, eps []models.PodcastEpisode) int {
	if len(eps) == 0 {
		return 0
	}
	known := map[string]bool{}
	if ids, err := database.GetAllPodcastEpisodeIds(podcastID); err == nil {
		for _, id := range ids {
			known[id] = true
		}
	}
	var fresh []models.PodcastEpisode
	for _, ep := range eps {
		if known[ep.YoutubeVideoId] {
			continue
		}
		known[ep.YoutubeVideoId] = true // feeds do repeat guids
		fresh = append(fresh, ep)
	}
	if len(fresh) > 0 {
		database.SavePlaylistEpisodes(fresh)
	}
	return len(fresh)
}
