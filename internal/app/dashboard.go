package app

import (
	"embed"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/autodl"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
	"ikoyhn/podcast-sponsorblock/internal/services/channelinfo"
	"ikoyhn/podcast-sponsorblock/internal/services/common"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/playlist"
	"ikoyhn/podcast-sponsorblock/internal/services/youtube"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	log "github.com/labstack/gommon/log"
)

//go:embed web/index.html
var webFS embed.FS

type dashboardEpisode struct {
	VideoId       string `json:"videoId"`
	Name          string `json:"name"`
	PublishedDate string `json:"publishedDate"`
	Downloaded    bool   `json:"downloaded"`
	Downloading   bool   `json:"downloading"`
	FailedCount   int    `json:"failedCount"`
	ImageUrl      string `json:"imageUrl"`
}

type dashboardPodcast struct {
	Id            string             `json:"id"`
	Name          string             `json:"name"`
	OriginalName  string             `json:"originalName"`
	CustomName    string             `json:"customName"`
	ArtistName    string             `json:"artistName"`
	Description   string             `json:"description"`
	ImageUrl      string             `json:"imageUrl"`
	Type          string             `json:"type"`
	FeedPath      string             `json:"feedPath"`
	EpisodeCount  int64              `json:"episodeCount"`
	AutoDownload  bool               `json:"autoDownload"`
	Subscribed    bool               `json:"subscribed"`
	LastFeedFetch int64              `json:"lastFeedFetch"`
	ParentId      string             `json:"parentId"`
	TitleFilter   string             `json:"titleFilter"`
	ExcludeFilter string             `json:"excludeFilter"`
	SbCategories  string             `json:"sbCategories"`
	ChannelId     string             `json:"channelId"`
	ChannelTitle  string             `json:"channelTitle"`
	ChannelThumb  string             `json:"channelThumb"`
	ChannelBanner string             `json:"channelBanner"`
	LastBuildDate string             `json:"lastBuildDate"`
	Episodes      []dashboardEpisode `json:"episodes"`
}

type addPodcastRequest struct {
	Input string `json:"input"`
}

var playlistIdRegex = regexp.MustCompile(`^(PL|UU|FL|OL|RD)[A-Za-z0-9_-]{10,}$`)
var channelIdRegex = regexp.MustCompile(`^UC[A-Za-z0-9_-]{20,}$`)

func registerDashboardRoutes(e *echo.Echo) {
	// Serve the dashboard UI
	e.GET("/", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		data, err := webFS.ReadFile("web/index.html")
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "UI not available")
		}
		return c.HTMLBlob(http.StatusOK, data)
	})

	// List all podcasts with their 5 most recent episodes + download status
	e.GET("/api/podcasts", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		podcasts, err := database.GetAllPodcasts()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
		result := make([]dashboardPodcast, 0, len(podcasts))
		for _, p := range podcasts {
			var podcastType, feedPath string
			var episodes []models.PodcastEpisode
			var err error
			var episodeCount int64

			if p.IsVirtual() {
				podcastType = "FILTER"
				feedPath = "/rss/" + p.Id
				episodes, err = database.GetRecentEpisodesFiltered(p.ParentId, p.TitleFilter, p.ExcludeTerms(), 5)
				episodeCount = database.CountEpisodesFiltered(p.ParentId, p.TitleFilter, p.ExcludeTerms())
				if p.CustomImage == "" && p.ImageUrl == "" {
					if parent := database.GetPodcast(p.ParentId); parent != nil {
						p.ImageUrl = parent.ImageUrl
					}
				}
			} else {
				podcastType = database.GetEpisodeType(p.Id)
				if podcastType == "" {
					if channelIdRegex.MatchString(p.Id) {
						podcastType = "CHANNEL"
					} else {
						podcastType = "PLAYLIST"
					}
				}
				feedPath = "/rss/" + p.Id
				if podcastType == "CHANNEL" {
					feedPath = "/channel/" + p.Id
				}
				episodes, err = database.GetRecentEpisodes(p.Id, 5)
				episodeCount = database.CountEpisodes(p.Id)
			}
			if err != nil {
				log.Error(err)
			}
			dashEpisodes := make([]dashboardEpisode, 0, len(episodes))
			for _, ep := range episodes {
				dashEpisodes = append(dashEpisodes, dashboardEpisode{
					VideoId:       ep.YoutubeVideoId,
					Name:          ep.EpisodeName,
					PublishedDate: ep.PublishedDate.Format(time.RFC3339),
					Downloaded:    database.FileExistsWithId(audioDirAbs, ep.YoutubeVideoId),
					Downloading:   autodl.IsDownloading(ep.YoutubeVideoId),
					FailedCount:   autodl.FailureCount(ep.YoutubeVideoId),
					ImageUrl:      ep.ImageUrl,
				})
			}

			imageUrl := p.ImageUrl
			if p.CustomImage != "" {
				imageUrl = "/covers/" + p.Id
			}

			result = append(result, dashboardPodcast{
				Id:            p.Id,
				Name:          p.DisplayName(),
				OriginalName:  p.PodcastName,
				CustomName:    p.CustomName,
				ArtistName:    p.ArtistName,
				Description:   p.Description,
				ImageUrl:      imageUrl,
				Type:          podcastType,
				FeedPath:      feedPath,
				EpisodeCount:  episodeCount,
				AutoDownload:  !p.AutoDownloadOff,
				Subscribed:    p.Subscribed,
				LastFeedFetch: p.LastFeedFetch,
				ParentId:      p.ParentId,
				TitleFilter:   p.TitleFilter,
				ExcludeFilter: p.ExcludeFilter,
				SbCategories:  p.SponsorblockCategories,
				ChannelId:     p.ChannelId,
				ChannelTitle:  p.ChannelTitle,
				ChannelThumb:  p.ChannelThumb,
				ChannelBanner: p.ChannelBanner,
				LastBuildDate: p.LastBuildDate,
				Episodes:      dashEpisodes,
			})
		}
		return c.JSON(http.StatusOK, result)
	})

	// Add a new podcast by YouTube URL, playlist ID, channel ID, or @handle
	e.POST("/api/podcasts", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		var req addPodcastRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
		}
		id, podcastType, err := resolveInput(strings.TrimSpace(req.Input))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		host := handler(c.Request())
		if podcastType == "CHANNEL" {
			channel.BuildChannelRssFeed(id, &models.RssRequestParams{}, host)
		} else {
			playlist.BuildPlaylistRssFeed(id, host)
		}

		p := database.GetPodcast(id)
		if p == nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity,
				"Could not create podcast — check the ID/URL and your YouTube API quota")
		}
		feedPath := "/rss/" + id
		if podcastType == "CHANNEL" {
			feedPath = "/channel/" + id
		}
		events.Info("Podcast added: %s (%s)", p.PodcastName, id)
		channelinfo.ResolveForPodcast(id)
		autodl.CheckPodcast(id)
		return c.JSON(http.StatusCreated, map[string]string{
			"id":       id,
			"name":     p.PodcastName,
			"type":     podcastType,
			"feedPath": feedPath,
		})
	})

	// Rename the republished feed (empty name reverts to the YouTube name)
	e.PATCH("/api/podcasts/:id", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		var req struct {
			Name         *string `json:"name"`
			AutoDownload *bool   `json:"autoDownload"`
			Subscribed   *bool   `json:"subscribed"`
			SbCategories *string `json:"sbCategories"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
		}
		id := c.Param("id")
		p := database.GetPodcast(id)
		if p == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if err := database.SetCustomName(id, name); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
			if name == "" {
				events.Info("Feed name reverted to YouTube name for %s", p.PodcastName)
			} else {
				events.Info("Feed renamed: %s → %s", p.PodcastName, name)
			}
		}
		if req.AutoDownload != nil {
			if err := database.SetAutoDownload(id, *req.AutoDownload); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
			state := "enabled"
			if !*req.AutoDownload {
				state = "disabled"
			}
			events.Info("Auto-download %s for %s", state, p.DisplayName())
		}
		if req.Subscribed != nil {
			if err := database.SetSubscribed(id, *req.Subscribed); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}
		if req.SbCategories != nil {
			cats := strings.TrimSpace(*req.SbCategories)
			if err := database.SetSponsorblockCategories(id, cats); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
			if cats == "" {
				events.Info("SponsorBlock categories reverted to global default for %s", p.DisplayName())
			} else {
				events.Info("SponsorBlock categories for %s set to: %s", p.DisplayName(), cats)
			}
		}
		return c.NoContent(http.StatusNoContent)
	})

	// Remove every feed belonging to a channel
	e.DELETE("/api/channels/:channelId", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		channelId := c.Param("channelId")
		podcasts := database.GetPodcastsByChannel(channelId)
		if len(podcasts) == 0 {
			return echo.NewHTTPError(http.StatusNotFound, "No podcasts for that channel")
		}
		removed := 0
		for _, p := range podcasts {
			for _, child := range database.GetChildFeeds(p.Id) {
				database.DeletePodcastAndEpisodes(child.Id)
				removed++
			}
			if err := database.DeletePodcastAndEpisodes(p.Id); err == nil {
				removed++
			}
		}
		events.Info("Removed %d feed(s) from channel %s", removed, channelId)
		return c.JSON(http.StatusOK, map[string]int{"removed": removed})
	})

	// Liveness probe (no auth — used by Docker HEALTHCHECK)
	e.GET("/healthz", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Aggregate stats for the dashboard header
	e.GET("/api/stats", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		files, bytes := autodl.StorageStats()
		return c.JSON(http.StatusOK, map[string]interface{}{
			"storedFiles":         files,
			"storedBytes":         bytes,
			"totalSkippedSeconds": database.TotalTimeSkipped(),
		})
	})

	// Paged, searchable episode browser (filter-aware for virtual feeds)
	e.GET("/api/podcasts/:id/episodes", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		p := database.GetPodcast(c.Param("id"))
		if p == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		sourceId, filter := p.Id, ""
		var exclude []string
		if p.IsVirtual() {
			sourceId, filter, exclude = p.ParentId, p.TitleFilter, p.ExcludeTerms()
		}
		offset, _ := strconv.Atoi(c.QueryParam("offset"))
		limit, _ := strconv.Atoi(c.QueryParam("limit"))
		if limit <= 0 || limit > 100 {
			limit = 50
		}
		episodes, total, err := database.SearchEpisodes(sourceId, filter, exclude, c.QueryParam("q"), offset, limit)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
		out := make([]dashboardEpisode, 0, len(episodes))
		for _, ep := range episodes {
			out = append(out, dashboardEpisode{
				VideoId:       ep.YoutubeVideoId,
				Name:          ep.EpisodeName,
				PublishedDate: ep.PublishedDate.Format(time.RFC3339),
				Downloaded:    database.FileExistsWithId(audioDirAbs, ep.YoutubeVideoId),
				Downloading:   autodl.IsDownloading(ep.YoutubeVideoId),
				FailedCount:   autodl.FailureCount(ep.YoutubeVideoId),
				ImageUrl:      ep.ImageUrl,
			})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"total": total, "episodes": out})
	})

	// Detect recurring series/show names within a podcast's episode titles
	e.GET("/api/podcasts/:id/series", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		p := database.GetPodcast(c.Param("id"))
		if p == nil || p.IsVirtual() {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		episodes, err := database.GetEpisodesFiltered(p.Id, "", nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, detectSeries(episodes, p.PodcastName))
	})

	// Create a filtered sub-feed from a detected (or custom) series name
	e.POST("/api/podcasts/:id/split", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		parent := database.GetPodcast(c.Param("id"))
		if parent == nil || parent.IsVirtual() {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		var req struct {
			Filter  string   `json:"filter"`
			Exclude []string `json:"exclude"`
			Name    string   `json:"name"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
		}
		filter := strings.TrimSpace(req.Filter)
		var exclude []string
		for _, t := range req.Exclude {
			if t = strings.TrimSpace(t); t != "" {
				exclude = append(exclude, t)
			}
		}
		if filter == "" && len(exclude) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "Provide a filter or exclusions")
		}
		if database.CountEpisodesFiltered(parent.Id, filter, exclude) == 0 {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, "No episodes match that filter")
		}

		name := strings.TrimSpace(req.Name)
		slugSrc := filter
		description := "Filtered feed of " + parent.DisplayName() + " — episodes matching \"" + filter + "\""
		if filter == "" {
			if name == "" {
				name = parent.DisplayName() + " (everything else)"
			}
			slugSrc = "everything-else"
			description = "Feed of " + parent.DisplayName() + " excluding: " + strings.Join(exclude, ", ")
		} else if name == "" {
			name = filter
			if len(exclude) > 0 {
				description += ", excluding: " + strings.Join(exclude, ", ")
			}
		}

		virtualId := parent.Id + "~" + slugify(slugSrc)
		if database.GetPodcast(virtualId) != nil {
			return echo.NewHTTPError(http.StatusConflict, "A feed for that filter already exists")
		}
		v := &models.Podcast{
			Id:            virtualId,
			ParentId:      parent.Id,
			TitleFilter:   filter,
			ExcludeFilter: strings.Join(exclude, "|"),
			PodcastName:   name,
			Description:   description,
			ImageUrl:      parent.ImageUrl,
			ArtistName:    parent.ArtistName,
			Explicit:      parent.Explicit,
			PostedDate:    parent.PostedDate,
		}
		database.SavePodcast(v)
		events.Info("Created filtered feed %q from %s", name, parent.DisplayName())
		return c.JSON(http.StatusCreated, map[string]string{
			"id":       virtualId,
			"name":     name,
			"feedPath": "/rss/" + virtualId,
		})
	})

	// OPML export of all feeds
	e.GET("/opml", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		podcasts, err := database.GetAllPodcasts()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		host := handler(c.Request())
		if pub := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); pub != "" && !isLocalRequest(c) {
			host = pub
		}
		tokenParam := ""
		if config.AppConfig.Authentication.Token != "" {
			tokenParam = "?token=" + config.AppConfig.Authentication.Token
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><opml version="2.0"><head><title>jojo-podcasts</title></head><body>`)
		for _, p := range podcasts {
			feedPath := "/rss/" + p.Id
			if !p.IsVirtual() && database.GetEpisodeType(p.Id) == "CHANNEL" {
				feedPath = "/channel/" + p.Id
			}
			b.WriteString(`<outline type="rss" text="` + xmlEscape(p.DisplayName()) +
				`" title="` + xmlEscape(p.DisplayName()) +
				`" xmlUrl="` + xmlEscape(host+feedPath+tokenParam) + `"/>`)
		}
		b.WriteString(`</body></opml>`)
		c.Response().Header().Set("Content-Disposition", `attachment; filename="jojo-podcasts.opml"`)
		return c.Blob(http.StatusOK, "text/x-opml", []byte(b.String()))
	})

	// List a channel's playlists so they can be subscribed to individually
	e.POST("/api/browse-playlists", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		var req addPodcastRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid request body")
		}
		input := strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(req.Input), "/"), "/playlists"))
		input = strings.TrimRight(input, "/")
		channelId, _, err := resolveInput(input)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Could not resolve a channel from that input")
		}
		if !channelIdRegex.MatchString(channelId) {
			return echo.NewHTTPError(http.StatusBadRequest, "That input is a playlist, not a channel")
		}
		type plEntry struct {
			Id        string `json:"id"`
			Title     string `json:"title"`
			Count     int64  `json:"count"`
			Thumbnail string `json:"thumbnail"`
		}

		// channel metadata: title, avatar, banner
		channelTitle, channelThumb, channelBanner := "", "", ""
		if chResp, err := youtube.YtService.Channels.
			List([]string{"snippet", "brandingSettings"}).Id(channelId).Do(); err == nil && len(chResp.Items) > 0 {
			ch := chResp.Items[0]
			channelTitle = ch.Snippet.Title
			if ch.Snippet.Thumbnails != nil && ch.Snippet.Thumbnails.Medium != nil {
				channelThumb = ch.Snippet.Thumbnails.Medium.Url
			}
			if ch.BrandingSettings != nil && ch.BrandingSettings.Image != nil {
				channelBanner = ch.BrandingSettings.Image.BannerExternalUrl
			}
		}

		var pods, lists []plEntry
		pageToken := ""
		for {
			call := youtube.YtService.Playlists.List([]string{"snippet", "contentDetails", "status"}).
				ChannelId(channelId).MaxResults(50)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			resp, err := call.Do()
			if err != nil {
				return echo.NewHTTPError(http.StatusBadGateway, "YouTube API error: "+err.Error())
			}
			for _, item := range resp.Items {
				thumb := ""
				if item.Snippet.Thumbnails != nil {
					if item.Snippet.Thumbnails.High != nil {
						thumb = item.Snippet.Thumbnails.High.Url
					} else if item.Snippet.Thumbnails.Medium != nil {
						thumb = item.Snippet.Thumbnails.Medium.Url
					}
				}
				entry := plEntry{
					Id:        item.Id,
					Title:     item.Snippet.Title,
					Count:     item.ContentDetails.ItemCount,
					Thumbnail: thumb,
				}
				if item.Status != nil && item.Status.PodcastStatus == "enabled" {
					pods = append(pods, entry)
				} else {
					lists = append(lists, entry)
				}
			}
			pageToken = resp.NextPageToken
			if pageToken == "" || len(pods)+len(lists) >= 300 {
				break
			}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"channelId":     channelId,
			"channelTitle":  channelTitle,
			"channelThumb":  channelThumb,
			"channelBanner": channelBanner,
			"podcasts":      pods,
			"playlists":     lists,
		})
	})

	// Serve custom cover art
	e.GET("/covers/:id", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		id := c.Param("id")
		if strings.ContainsAny(id, "/\\.") {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid id")
		}
		coversDir := filepath.Join(config.AppConfig.Setup.ConfigDir, "covers")
		file := database.FindFileWithId(coversDir, id)
		if file == "" {
			return echo.NewHTTPError(http.StatusNotFound, "No custom cover")
		}
		return c.File(file)
	})

	// Upload custom cover art for a podcast
	e.POST("/api/podcasts/:id/cover", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		id := c.Param("id")
		if database.GetPodcast(id) == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		fh, err := c.FormFile("image")
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "No image file in request")
		}
		if fh.Size > 10*1024*1024 {
			return echo.NewHTTPError(http.StatusBadRequest, "Image too large (max 10 MB)")
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
			return echo.NewHTTPError(http.StatusBadRequest, "Use a .jpg, .png, or .webp image")
		}
		coversDir := filepath.Join(config.AppConfig.Setup.ConfigDir, "covers")
		if err := os.MkdirAll(coversDir, 0o755); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// remove any previous cover for this podcast
		if old := database.FindFileWithId(coversDir, id); old != "" {
			os.Remove(old)
		}
		src, err := fh.Open()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		defer src.Close()
		destPath := filepath.Join(coversDir, id+ext)
		dest, err := os.Create(destPath)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		defer dest.Close()
		if _, err := io.Copy(dest, src); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if err := database.SetCustomImage(id, id+ext); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		events.Info("Custom cover art uploaded for %s", id)
		return c.NoContent(http.StatusNoContent)
	})

	// Remove custom cover art (revert to YouTube artwork)
	e.DELETE("/api/podcasts/:id/cover", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		id := c.Param("id")
		coversDir := filepath.Join(config.AppConfig.Setup.ConfigDir, "covers")
		if old := database.FindFileWithId(coversDir, id); old != "" {
			os.Remove(old)
		}
		if err := database.SetCustomImage(id, ""); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		events.Info("Custom cover art removed for %s", id)
		return c.NoContent(http.StatusNoContent)
	})

	// UI config (e.g. public tunnel URL for remote subscribe links)
	e.GET("/api/config", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		resp := map[string]string{
			"publicUrl": strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
			"sbDefault": config.AppConfig.Ytdlp.SponsorBlockCategories,
		}
		// Only reveal the token to local-network clients, so the local
		// dashboard can build remote (tunnel) links that include it
		if isLocalRequest(c) {
			resp["token"] = config.AppConfig.Authentication.Token
		}
		return c.JSON(http.StatusOK, resp)
	})

	// Trigger a download of one episode
	e.POST("/api/episodes/:videoId/download", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		videoId := c.Param("videoId")
		if !common.IsValidParam(videoId) {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid video id")
		}
		ep, err := database.GetEpisodeByVideoId(videoId)
		if err != nil || ep == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Episode not found")
		}
		go autodl.Download(videoId, ep.EpisodeName)
		return c.JSON(http.StatusAccepted, map[string]string{"status": "downloading"})
	})

	// Delete a downloaded episode audio file
	e.DELETE("/api/episodes/:videoId/file", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		videoId := c.Param("videoId")
		if !common.IsValidParam(videoId) {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid video id")
		}
		audioDirAbs, _ := filepath.Abs(config.AppConfig.Setup.AudioDir)
		filePath := database.FindFileWithId(audioDirAbs, videoId)
		if filePath == "" {
			return echo.NewHTTPError(http.StatusNotFound, "No downloaded file for this episode")
		}
		if err := os.Remove(filePath); err != nil {
			events.Error("Failed to delete file for %s: %v", videoId, err)
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		name := videoId
		if ep, err := database.GetEpisodeByVideoId(videoId); err == nil && ep != nil {
			name = ep.EpisodeName
		}
		events.Info("Deleted audio file: %s", name)
		return c.NoContent(http.StatusNoContent)
	})

	// Recent activity / error log for debugging
	e.GET("/api/events", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, events.List())
	})

	// Remove a podcast subscription (episode records + podcast row; audio files kept)
	e.DELETE("/api/podcasts/:id", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		id := c.Param("id")
		if database.GetPodcast(id) == nil {
			return echo.NewHTTPError(http.StatusNotFound, "Podcast not found")
		}
		// deleting a parent also removes its filtered sub-feeds
		for _, child := range database.GetChildFeeds(id) {
			database.DeletePodcastAndEpisodes(child.Id)
		}
		if err := database.DeletePodcastAndEpisodes(id); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.NoContent(http.StatusNoContent)
	})
}

// resolveInput turns a user-supplied string (URL, ID, or @handle) into a
// podcast ID and type (PLAYLIST or CHANNEL)
func resolveInput(input string) (string, string, error) {
	if input == "" {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "Input is empty")
	}

	// Full URL handling
	if strings.Contains(input, "youtube.com") || strings.Contains(input, "youtu.be") {
		raw := input
		if !strings.HasPrefix(raw, "http") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", echo.NewHTTPError(http.StatusBadRequest, "Could not parse URL")
		}
		// playlist link: ...?list=<id>
		if list := u.Query().Get("list"); list != "" {
			return list, "PLAYLIST", nil
		}
		// channel link: /channel/UC...
		if strings.HasPrefix(u.Path, "/channel/") {
			id := strings.Trim(strings.TrimPrefix(u.Path, "/channel/"), "/")
			if channelIdRegex.MatchString(id) {
				return id, "CHANNEL", nil
			}
		}
		// handle link: /@handle
		if strings.HasPrefix(u.Path, "/@") {
			return resolveHandle(strings.Trim(strings.TrimPrefix(u.Path, "/"), "/"))
		}
		return "", "", echo.NewHTTPError(http.StatusBadRequest,
			"Unsupported YouTube URL — use a playlist, channel, or @handle link")
	}

	// Raw values
	if strings.HasPrefix(input, "@") {
		return resolveHandle(input)
	}
	if channelIdRegex.MatchString(input) {
		return input, "CHANNEL", nil
	}
	if playlistIdRegex.MatchString(input) {
		return input, "PLAYLIST", nil
	}
	return "", "", echo.NewHTTPError(http.StatusBadRequest,
		"Unrecognized input — provide a playlist ID (PL...), channel ID (UC...), @handle, or YouTube URL")
}

type seriesCandidate struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var trailingNumbers = regexp.MustCompile(`[\s#\-–:.]*[\d][\d.\s/]*$`)

// detectSeries finds recurring show/series names in episode titles by
// grouping on prefixes (before separators) and suffixes (after " - ")
func detectSeries(episodes []models.PodcastEpisode, podcastName string) []seriesCandidate {
	counts := map[string]int{}
	seps := []string{" - ", " – ", " — ", ": ", " | "}
	addCandidate := func(s string) {
		s = trailingNumbers.ReplaceAllString(strings.TrimSpace(s), "")
		s = strings.TrimSpace(s)
		if len(s) >= 4 && !strings.EqualFold(s, podcastName) {
			counts[s]++
		}
	}
	for _, ep := range episodes {
		title := ep.EpisodeName
		// prefix before the first separator
		firstIdx := -1
		for _, sep := range seps {
			if i := strings.Index(title, sep); i > 3 && (firstIdx == -1 || i < firstIdx) {
				firstIdx = i
			}
		}
		if firstIdx > 0 {
			addCandidate(title[:firstIdx])
		}
		// suffix after the last " - " (common "<topic> - <show name> <date>" pattern)
		for _, sep := range []string{" - ", " – ", " — "} {
			if i := strings.LastIndex(title, sep); i > 0 && i+len(sep) < len(title) {
				addCandidate(title[i+len(sep):])
			}
		}
	}
	var out []seriesCandidate
	for name, count := range counts {
		if count >= 3 {
			out = append(out, seriesCandidate{Name: name, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > 15 {
		out = out[:15]
	}
	return out
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

func slugify(s string) string {
	out := slugUnsafe.ReplaceAllString(s, "-")
	out = strings.Trim(out, "-")
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		out = "filter"
	}
	return out
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// resolveHandle looks up a channel ID for a @handle via the YouTube API
func resolveHandle(handle string) (string, string, error) {
	resp, err := youtube.YtService.Channels.List([]string{"id"}).ForHandle(handle).Do()
	if err != nil || len(resp.Items) == 0 {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "Could not resolve handle "+handle)
	}
	return resp.Items[0].Id, "CHANNEL", nil
}
