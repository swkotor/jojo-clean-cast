package channelinfo

import (
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/youtube"
	"regexp"
	"sync"
	"time"

	log "github.com/labstack/gommon/log"
)

var channelIdRegex = regexp.MustCompile(`^UC[A-Za-z0-9_-]{20,}$`)

// cache of channel metadata so a backfill of many podcasts on the same
// channel costs one API call
type chanMeta struct {
	Title  string
	Thumb  string
	Banner string
}

var cache sync.Map // channelId -> chanMeta

// Lookup fetches (and caches) channel metadata
func Lookup(channelId string) (title, thumb, banner string) {
	if v, ok := cache.Load(channelId); ok {
		m := v.(chanMeta)
		return m.Title, m.Thumb, m.Banner
	}
	resp, err := youtube.YtService.Channels.
		List([]string{"snippet", "brandingSettings"}).Id(channelId).Do()
	if err != nil || len(resp.Items) == 0 {
		return "", "", ""
	}
	ch := resp.Items[0]
	title = ch.Snippet.Title
	if ch.Snippet.Thumbnails != nil {
		if ch.Snippet.Thumbnails.High != nil {
			thumb = ch.Snippet.Thumbnails.High.Url
		} else if ch.Snippet.Thumbnails.Medium != nil {
			thumb = ch.Snippet.Thumbnails.Medium.Url
		}
	}
	if ch.BrandingSettings != nil && ch.BrandingSettings.Image != nil {
		banner = ch.BrandingSettings.Image.BannerExternalUrl
	}
	cache.Store(channelId, chanMeta{title, thumb, banner})
	return
}

// ResolveForPodcast finds the owning channel of a podcast (playlist or
// channel feed) and stores its metadata
func ResolveForPodcast(podcastId string) {
	if podcastId == "" {
		return
	}
	channelId := podcastId
	playlistTitle, playlistThumb := "", ""
	if !channelIdRegex.MatchString(podcastId) {
		// playlist → look up the owning channel
		resp, err := youtube.YtService.Playlists.List([]string{"snippet"}).Id(podcastId).Do()
		if err != nil || len(resp.Items) == 0 {
			return
		}
		sn := resp.Items[0].Snippet
		channelId = sn.ChannelId
		playlistTitle = sn.Title
		if sn.Thumbnails != nil {
			if sn.Thumbnails.Maxres != nil {
				playlistThumb = sn.Thumbnails.Maxres.Url
			} else if sn.Thumbnails.Standard != nil {
				playlistThumb = sn.Thumbnails.Standard.Url
			} else if sn.Thumbnails.High != nil {
				playlistThumb = sn.Thumbnails.High.Url
			}
		}
	}
	title, thumb, banner := Lookup(channelId)
	if title == "" {
		return
	}

	// Older versions named playlist feeds after the owning channel — if the
	// stored name is just the channel name, correct it to the playlist title
	if playlistTitle != "" && playlistTitle != title {
		if p := database.GetPodcast(podcastId); p != nil && p.PodcastName == title {
			if err := database.UpdatePodcastNameAndImage(podcastId, playlistTitle, playlistThumb); err != nil {
				log.Errorf("[CHANNELINFO] %v", err)
			} else {
				events.Info("Renamed feed to its playlist title: %s → %s", title, playlistTitle)
			}
		}
	}
	if err := database.SetChannelInfo(podcastId, channelId, title, thumb, banner); err != nil {
		log.Errorf("[CHANNELINFO] %v", err)
	}
}

// BackfillAll resolves channel info for every podcast that lacks it.
// Runs in the background so startup is not delayed.
func BackfillAll() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				events.Error("Channel backfill crashed: %v", r)
			}
		}()
		time.Sleep(10 * time.Second)
		podcasts, err := database.GetAllPodcasts()
		if err != nil {
			return
		}
		done := 0
		for _, p := range podcasts {
			if p.IsVirtual() {
				continue
			}
			// resolve if we have no channel info yet, or if the feed is still
			// named after its channel (older naming bug for playlist feeds)
			needsName := p.ChannelTitle != "" && p.PodcastName == p.ChannelTitle &&
				!channelIdRegex.MatchString(p.Id)
			if p.ChannelId != "" && !needsName {
				continue
			}
			ResolveForPodcast(p.Id)
			done++
			time.Sleep(300 * time.Millisecond)
		}
		if done > 0 {
			events.Info("Grouped %d podcast(s) under their YouTube channels", done)
		}
	}()
}
