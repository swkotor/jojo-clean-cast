package app

import (
	"embed"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/autodl"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
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
			podcastType := database.GetEpisodeType(p.Id)
			if podcastType == "" {
				if channelIdRegex.MatchString(p.Id) {
					podcastType = "CHANNEL"
				} else {
					podcastType = "PLAYLIST"
				}
			}
			feedPath := "/rss/" + p.Id
			if podcastType == "CHANNEL" {
				feedPath = "/channel/" + p.Id
			}

			episodes, err := database.GetRecentEpisodes(p.Id, 5)
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
				EpisodeCount:  database.CountEpisodes(p.Id),
				AutoDownload:  !p.AutoDownloadOff,
				Subscribed:    p.Subscribed,
				LastFeedFetch: p.LastFeedFetch,
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
		return c.NoContent(http.StatusNoContent)
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

// resolveHandle looks up a channel ID for a @handle via the YouTube API
func resolveHandle(handle string) (string, string, error) {
	resp, err := youtube.YtService.Channels.List([]string{"id"}).ForHandle(handle).Do()
	if err != nil || len(resp.Items) == 0 {
		return "", "", echo.NewHTTPError(http.StatusBadRequest, "Could not resolve handle "+handle)
	}
	return resp.Items[0].Id, "CHANNEL", nil
}
