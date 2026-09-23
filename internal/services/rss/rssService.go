package rss

import (
	"fmt"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/enum"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/generator"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/labstack/gommon/log"
)

func GenerateRssFeed(podcast models.Podcast, host string, podcastType enum.PodcastType) []byte {
	log.Info("[RSS FEED] Generating RSS Feed...")

	podcastLink := "https://www.youtube.com/playlist?list=" + podcast.Id

	if podcastType == enum.CHANNEL {
		podcastLink = "https://www.youtube.com/channel/" + podcast.Id
	}

	now := time.Now()
	ytPodcast := generator.New(podcast.DisplayName(), podcastLink, podcast.Description, &now)
	// Prefer artwork we serve ourselves: square, clean URL, always reachable
	// (YouTube's signed CDN URLs are not, and the old filename rewrite 404s).
	coverUrl := podcast.ImageUrl
	if podcast.CustomImage != "" || podcast.AutoImage != "" {
		coverUrl = host + "/covers/" + podcast.Id
		if config.AppConfig.Authentication.Token != "" {
			coverUrl += "?token=" + config.AppConfig.Authentication.Token
		}
	}
	ytPodcast.AddImage(coverUrl)
	ytPodcast.AddCategory(podcast.Category, []string{""})
	ytPodcast.Docs = "http://www.rssboard.org/rss-specification"
	ytPodcast.IAuthor = podcast.ArtistName

	if podcast.PodcastEpisodes != nil {
		for _, podcastEpisode := range podcast.PodcastEpisodes {
			if (podcastEpisode.Type == "CHANNEL" && podcastEpisode.Duration.Seconds() < 120) || podcastEpisode.EpisodeName == "Private video" || podcastEpisode.EpisodeDescription == "This video is private." {
				continue
			}
			mediaUrl := host + "/media/" + podcastEpisode.YoutubeVideoId

			if config.AppConfig.Authentication.Token != "" {
				mediaUrl = mediaUrl + "?token=" + config.AppConfig.Authentication.Token
			}
			enclosure := generator.Enclosure{
				URL:    mediaUrl,
				Length: 0,
				Type:   generator.M4A,
			}
			// length is a required RSS field and drives the download progress
			// bar in Apple Podcasts and Overcast; it was always 0. Fill it in
			// (and match the MIME type to the extension) whenever the file is
			// already on disk.
			if fp := database.FindFileWithId(config.AppConfig.Setup.AudioDir, podcastEpisode.YoutubeVideoId); fp != "" {
				if st, err := os.Stat(fp); err == nil {
					enclosure.Length = st.Size()
				}
				switch strings.ToLower(filepath.Ext(fp)) {
				case ".mp3":
					enclosure.Type = generator.MP3
				case ".m4a", ".mp4":
					enclosure.Type = generator.M4A
				}
			}

			// NO pre-escaping here. Description is chardata on the Item struct,
			// so encoding/xml escapes it when marshalling; escaping first meant
			// every feed shipped doubly-escaped show notes and listeners saw
			// literal "&#39;" and "&#xA;" instead of quotes and line breaks.
			description := podcastEpisode.EpisodeDescription
			if strings.TrimSpace(description) == "" {
				// AddItem rejects an empty description, silently dropping the
				// episode from the feed. Fall back to the title.
				description = podcastEpisode.EpisodeName
			}

			podcastItem := generator.Item{
				Title:       podcastEpisode.EpisodeName,
				Description: description,
				IDuration:   fmt.Sprintf("%d", int(podcastEpisode.Duration.Seconds())),
				GUID: struct {
					Value       string `xml:",chardata"`
					IsPermaLink bool   `xml:"isPermaLink,attr"`
				}{
					Value:       podcastEpisode.YoutubeVideoId,
					IsPermaLink: false,
				},
				Enclosure: &enclosure,
				PubDate:   &podcastEpisode.PublishedDate,
			}

			// Add image if available
			if podcastEpisode.ImageUrl != "" {
				podcastItem.IImage = struct {
					Href string `xml:"href,attr"`
				}{
					Href: podcastEpisode.ImageUrl,
				}
			}

			if _, err := ytPodcast.AddItem(podcastItem); err != nil {
				log.Warnf("[RSS] Dropped episode %s from feed %s: %v",
					podcastEpisode.YoutubeVideoId, podcast.DisplayName(), err)
			}
		}
	}

	return ytPodcast.Bytes()
}

func BuildPodcast(podcast models.Podcast, allItems []models.PodcastEpisode) models.Podcast {
	podcast.PodcastEpisodes = allItems
	return podcast
}

func transformArtworkURL(artworkURL string, newHeight int, newWidth int) string {
	parsedURL, err := url.Parse(artworkURL)
	if err != nil {
		return ""
	}

	log.Debug("[RSS FEED] Transforming image url...", artworkURL)
	pathComponents := strings.Split(parsedURL.Path, "/")
	lastComponent := pathComponents[len(pathComponents)-1]
	ext := filepath.Ext(lastComponent)
	if ext == "" {
		log.Debug("[RSS FEED] No file extension found, returning original URL")
		return artworkURL
	}

	newFilename := fmt.Sprintf("%dx%d%s", newHeight, newWidth, ext)
	pathComponents[len(pathComponents)-1] = newFilename
	newPath := strings.Join(pathComponents, "/")

	newURL := url.URL{
		Scheme:   parsedURL.Scheme,
		Host:     parsedURL.Host,
		Path:     newPath,
		RawQuery: parsedURL.RawQuery,
		Fragment: parsedURL.Fragment,
	}

	log.Debug("[RSS FEED] New image url: ", newURL.String())

	return newURL.String()
}
