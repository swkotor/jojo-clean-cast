package models

import (
	"ikoyhn/podcast-sponsorblock/internal/enum"
	"strings"
	"time"

	log "github.com/labstack/gommon/log"
	"google.golang.org/api/youtube/v3"
)

type PodcastEpisode struct {
	Id                 int32     `gorm:"autoIncrement;primary_key;not null"`
	YoutubeVideoId     string    `json:"youtube_video_id" gorm:"index:youtubevideoid_type"`
	EpisodeName        string    `json:"episode_name"`
	EpisodeDescription string    `json:"episode_description"`
	PublishedDate      time.Time `json:"published_date" gorm:"index:idx_ep_podcast_published,priority:2"`
	Type               string    `json:"type" gorm:"index:youtubevideoid_type_channelid_type"`
	// `foreignkey` is not an index. Every hot path filters on podcast_id and
	// orders by published_date (feed builds, the 30-minute poller, pruning), and
	// without this each one scanned the whole episode table and re-sorted it.
	PodcastId string        `json:"podcast_id" gorm:"foreignkey:PodcastId;association_foreignkey:Id;index:idx_ep_podcast_published,priority:1"`
	ImageUrl  string        `json:"image_url"`
	Duration  time.Duration `json:"duration"`
	// RSS-sourced episodes: where the audio lives, and the feed's stable id.
	// YoutubeVideoId doubles as the generic episode key for these (a synthetic
	// "rss_<hash>"), so downloads, media URLs and playback history need no
	// special-casing.
	EnclosureUrl string `json:"enclosure_url"`
	Guid         string `json:"guid" gorm:"index"`
}

type Podcast struct {
	AppleId         string           `json:"apple_id"`
	Id              string           `json:"id" gorm:"primary_key"`
	PodcastName     string           `json:"podcast_name"`
	Description     string           `json:"description"`
	Category        string           `json:"category"`
	PostedDate      string           `json:"posted_date"`
	ImageUrl        string           `json:"image_url"`
	LastBuildDate   string           `json:"last_build_date"`
	PodcastEpisodes []PodcastEpisode `json:"podcast_episodes"`
	ArtistName      string           `json:"artist_name"`
	Explicit        string           `json:"explicit"`
	CustomName      string           `json:"custom_name"`
	AutoDownloadOff bool             `json:"auto_download_off"`
	CustomImage     string           `json:"custom_image"`
	// Locally cached YouTube artwork (filename in <config>/art)
	AutoImage     string `json:"auto_image"`
	Subscribed    bool   `json:"subscribed"`
	LastFeedFetch int64  `json:"last_feed_fetch"`
	// Filtered sub-feed support: a "virtual" podcast that republishes a
	// title-filtered subset of its parent's episodes
	ParentId    string `json:"parent_id"`
	TitleFilter string `json:"title_filter"`
	// Comma-separated terms; episodes whose titles contain any of them are
	// excluded from this feed (used for "everything else" feeds)
	ExcludeFilter string `json:"exclude_filter"`
	// Per-podcast SponsorBlock category override (comma-separated)
	SponsorblockCategories string `json:"sponsorblock_categories"`
	// Owning YouTube channel (for grouping podcasts/playlists in the UI)
	ChannelId     string `json:"channel_id"`
	ChannelTitle  string `json:"channel_title"`
	ChannelThumb  string `json:"channel_thumb"`
	ChannelBanner string `json:"channel_banner"`
	// Where episodes are pulled from: "youtube" (default) or "rss". A podcast
	// may carry BOTH a YouTube id and a feed url — this picks which one is
	// used, so a show published in both places can be switched over without
	// being re-added.
	SourceType string `json:"source_type"`
	FeedUrl    string `json:"feed_url"`
}

// Source reports the effective episode source, defaulting to YouTube. An "rss"
// preference without a feed url falls back rather than leaving the podcast
// unable to fetch anything.
func (p *Podcast) Source() string {
	if p.SourceType == "rss" && strings.TrimSpace(p.FeedUrl) != "" {
		return "rss"
	}
	return "youtube"
}

// IsRss is shorthand for Source() == "rss".
func (p *Podcast) IsRss() bool { return p.Source() == "rss" }

// IsVirtual reports whether this podcast is a filtered sub-feed
func (p *Podcast) IsVirtual() bool {
	return p.ParentId != ""
}

// ExcludeTerms returns the exclusion terms as a slice
func (p *Podcast) ExcludeTerms() []string {
	if strings.TrimSpace(p.ExcludeFilter) == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(p.ExcludeFilter, "|") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// DisplayName returns the user-defined name if set, otherwise the YouTube name
func (p *Podcast) DisplayName() string {
	if p.CustomName != "" {
		return p.CustomName
	}
	return p.PodcastName
}

type EpisodePlaybackHistory struct {
	YoutubeVideoId string `json:"youtube_video_id" gorm:"primary_key"`
	LastAccessDate int64  `json:"last_access_date"`
	// How much SponsorBlock removed from the file currently on disk. Written
	// when the file is downloaded; it says nothing about the user.
	TotalTimeSkipped float64 `json:"total_time_skipped"`
	// Whether a device has actually FETCHED this episode. Only /media sets it.
	// The distinction matters: "delete once the listener has it" and "we know
	// what was cut from this file" are different facts, and conflating them
	// meant every downloaded episode was treated as already-listened-to and
	// deleted hours later, un-downloaded.
	Served bool `json:"served"`
}

func NewPodcastEpisode(youtubeVideo *youtube.Video, duration time.Duration, podcastType enum.PodcastType, podcastId string) PodcastEpisode {
	publishedAt, err := time.Parse("2006-01-02T15:04:05Z07:00", youtubeVideo.Snippet.PublishedAt)
	if err != nil {
		log.Error(err)
	}

	imageUrl := ""
	if youtubeVideo.Snippet.Thumbnails.Maxres != nil {
		imageUrl = youtubeVideo.Snippet.Thumbnails.Maxres.Url
	} else if youtubeVideo.Snippet.Thumbnails.Standard != nil {
		imageUrl = youtubeVideo.Snippet.Thumbnails.Standard.Url
	} else if youtubeVideo.Snippet.Thumbnails.High != nil {
		imageUrl = youtubeVideo.Snippet.Thumbnails.High.Url
	} else if youtubeVideo.Snippet.Thumbnails.Default != nil {
		imageUrl = youtubeVideo.Snippet.Thumbnails.Default.Url
	}

	return PodcastEpisode{
		YoutubeVideoId:     youtubeVideo.Id,
		EpisodeName:        youtubeVideo.Snippet.Title,
		EpisodeDescription: youtubeVideo.Snippet.Description,
		PublishedDate:      publishedAt,
		Type:               string(podcastType),
		PodcastId:          podcastId,
		Duration:           duration,
		ImageUrl:           imageUrl,
	}
}
