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
	// Start from everything already stored, so every setting the user has
	// made (ad-text strip, filters, channel, custom name and art, subscription,
	// dates) survives a re-add. Only the feed-derived fields are overwritten.
	p := &models.Podcast{}
	if existing != nil {
		*p = *existing
	}
	p.Id = id
	p.PodcastName = f.Title
	p.Description = f.Description
	p.ArtistName = f.Author
	p.ImageUrl = f.ImageURL
	p.Category = f.Category
	p.Explicit = f.Explicit
	p.SourceType = "rss"
	p.FeedUrl = feedURL
	p.LastFeedFetch = time.Now().Unix()
	if existing != nil {
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
