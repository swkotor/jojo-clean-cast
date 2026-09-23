package app

import (
	"crypto/subtle"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"ikoyhn/podcast-sponsorblock/internal/services/autodl"
	"ikoyhn/podcast-sponsorblock/internal/services/channel"
	"ikoyhn/podcast-sponsorblock/internal/services/common"
	"ikoyhn/podcast-sponsorblock/internal/services/downloader"
	"ikoyhn/podcast-sponsorblock/internal/services/filterfeed"
	"ikoyhn/podcast-sponsorblock/internal/services/playlist"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	log "github.com/labstack/gommon/log"
	"github.com/robfig/cron"
)

func registerRoutes(e *echo.Echo) {
	registerDashboardRoutes(e)

	e.GET("/channel/:channelId", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		rssRequestParams, err := validateQueryParams(c)
		if err != nil {
			return err
		}
		database.TouchFeedFetch(c.Param("channelId"), time.Now().Unix())
		data := channel.BuildChannelRssFeed(c.Param("channelId"), rssRequestParams, handler(c.Request()))
		if data == nil {
			return echo.NewHTTPError(http.StatusBadGateway, "Could not build channel feed")
		}
		c.Response().Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		c.Response().Header().Set("Content-Length", strconv.Itoa(len(data)))
		c.Response().Header().Del("Transfer-Encoding")
		return c.Blob(http.StatusOK, "application/rss+xml; charset=utf-8", data)
	})

	e.GET("/rss/:youtubePlaylistId", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}
		if _, err := validateQueryParams(c); err != nil {
			return err
		}
		playlistId := strings.Split(c.Param("youtubePlaylistId"), "&")[0]
		database.TouchFeedFetch(playlistId, time.Now().Unix())
		var data []byte
		if strings.Contains(playlistId, "~") {
			data = filterfeed.BuildFilteredRssFeed(playlistId, handler(c.Request()))
		} else {
			data = playlist.BuildPlaylistRssFeed(playlistId, handler(c.Request()))
		}
		if data == nil {
			return echo.NewHTTPError(http.StatusBadGateway, "Could not build playlist feed")
		}
		c.Response().Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		c.Response().Header().Set("Content-Length", strconv.Itoa(len(data)))
		c.Response().Header().Del("Transfer-Encoding")
		return c.Blob(http.StatusOK, "application/rss+xml; charset=utf-8", data)
	})

	e.GET("/media/:youtubeVideoId", func(c echo.Context) error {
		if err := checkAuthentication(c); err != nil {
			return err
		}

		youtubeVideoId := c.Param("youtubeVideoId")
		if strings.Contains(youtubeVideoId, "/") || strings.Contains(youtubeVideoId, "\\") || strings.Contains(youtubeVideoId, "..") {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid file name")
		}
		// IsValidID, not IsValidParam: this value becomes a yt-dlp output
		// template and is appended to a YouTube URL, so '%', '&' and '?' must
		// not survive - IsValidParam only rejects slashes and "..".
		if !common.IsValidID(youtubeVideoId) {
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid video id")
		}

		audioDirAbs, err := filepath.Abs(config.AppConfig.Setup.AudioDir)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Server config error")
		}

		filePath := database.FindFileWithId(audioDirAbs, youtubeVideoId)
		file, err := os.Open(filePath)

		// A podcast client fetches a large episode over MANY range requests.
		// Re-downloading here — which the old code did whenever SponsorBlock's
		// segment total differed from the stored one — replaces the file
		// mid-fetch, so the client stitches together byte ranges from two
		// different cuts of the show and you hear audio from somewhere else
		// (music beds from a removed ad, most audibly). An episode that is
		// already on disk is therefore ALWAYS served as-is; keeping its cuts
		// current is the background poller's job, and it only does it when the
		// episode has been idle long enough that nobody is mid-download.
		if file == nil || err != nil {
			if file != nil {
				file.Close()
			}
			// Bounded, and cancellable by the client. An unbounded receive
			// here meant a wedged yt-dlp hung the request forever, and every
			// retry from the podcast client piled up another stuck handler.
			select {
			case <-downloader.GetYoutubeVideo(youtubeVideoId, false):
			case <-c.Request().Context().Done():
				return echo.NewHTTPError(http.StatusRequestTimeout, "Client went away")
			case <-time.After(30 * time.Minute):
				log.Errorf("[MEDIA] Timed out waiting for %s", youtubeVideoId)
				return echo.NewHTTPError(http.StatusGatewayTimeout, "Episode still downloading, try again shortly")
			}
			filePath = database.FindFileWithId(audioDirAbs, youtubeVideoId)
			file, err = os.Open(filePath)
			if err != nil || file == nil {
				// fork: count this as a failed attempt so the auto-downloader
				// backs off and RETRIES it later, and show the failure badge
				// on the dashboard. Crucially, playback history is NOT touched
				// on this path (see below).
				autodl.NoteFailure(youtubeVideoId)
				log.Errorf("[MEDIA] No file available for %s: %v", youtubeVideoId, err)
				return echo.NewHTTPError(http.StatusNotFound, "Episode unavailable")
			}
		}

		file.Close()

		// Only the ACCESS time is refreshed here, and only once the file is
		// really about to be served. Overwriting the skip baseline on a serve
		// would erase the difference the drift check exists to spot — and
		// touching history BEFORE the download (as this used to) created a
		// playback-history row even when the download FAILED, which the
		// auto-downloader read as "already served, skip" and the episode was
		// then never fetched again without manual intervention.
		database.TouchEpisodeAccess(youtubeVideoId)

		// Always serve through http.ServeFile: it sets Content-Length and
		// Last-Modified and handles Range/If-Range correctly. That matters
		// because an episode can be re-cut (new SponsorBlock segments) while a
		// client is still fetching it — with If-Range the client restarts
		// cleanly instead of splicing bytes from two different versions, which
		// is heard as audio jumping to another part of the show.
		c.Response().Header().Set("Content-Type", "audio/mp4")
		c.Response().Header().Set("Accept-Ranges", "bytes")
		http.ServeFile(c.Response().Writer, c.Request(), filePath)
		return nil
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	host := os.Getenv("HOST")

	log.Debug("Starting server on " + host + ": " + port)
	e.Logger.Fatal(e.Start(host + ":" + port))

}

func validateQueryParams(c echo.Context) (*models.RssRequestParams, error) {
	limitVar := c.Request().URL.Query().Get("limit")
	dateVar := c.Request().URL.Query().Get("date")
	// Validate whichever id THIS route actually carries. This used to read
	// c.Param("channelId") unconditionally, so on /rss/:youtubePlaylistId it
	// checked an empty string and the real id went through unvalidated.
	for _, name := range []string{"channelId", "youtubePlaylistId"} {
		if v := c.Param(name); v != "" && !common.IsValidParam(v) {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid id")
		}
	}
	if limitVar != "" && dateVar != "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid parameters")
	}

	if limitVar != "" {
		limitInt, err := strconv.Atoi(limitVar)
		if err != nil || limitInt < 1 {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid limit")
		}
		if limitInt > 1000 {
			limitInt = 1000
		}
		return &models.RssRequestParams{Limit: &limitInt, Date: nil}, nil
	}

	if dateVar != "" {
		parsedDate, err := time.Parse("01-02-2006", dateVar)
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "Invalid date, expected MM-DD-YYYY")
		}
		return &models.RssRequestParams{Limit: nil, Date: &parsedDate}, nil
	}
	return &models.RssRequestParams{Limit: nil, Date: nil}, nil
}

func setupCron() {
	cronSchedule := "0 0 * * 0"
	if config.AppConfig.Setup.Cron != "" {
		cronSchedule = config.AppConfig.Setup.Cron
	}

	schedule, err := cron.ParseStandard(cronSchedule)
	if err != nil {
		log.Errorf("[CRON] Invalid cron schedule %q (%v), falling back to weekly", cronSchedule, err)
		schedule, _ = cron.ParseStandard("0 0 * * 0")
	}
	c := cron.New()
	c.Schedule(schedule, cron.FuncJob(database.DeletePodcastCronJob))
	c.Start()
}

func setupHandlers(e *echo.Echo) {
	hostMiddleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if value, ok := os.LookupEnv("TRUSTED_HOSTS"); ok && value != "" {
				log.Info("[AUTH] Checking hosts...")
				host := c.Request().Host
				if !common.Contains(strings.Split(value, ","), host) {
					log.Error("[AUTH] Invalid host")
					return echo.NewHTTPError(http.StatusForbidden, "Forbidden")
				}
			}
			return next(c)
		}
	}

	if value, ok := os.LookupEnv("TRUSTED_HOSTS"); ok && value != "" {
		e.Use(hostMiddleware)
	}

	// Reject oversized bodies on Content-Length, before anything is read.
	// Cover upload checked the size only AFTER c.FormFile had already spooled
	// the whole multipart body to memory and disk.
	e.Use(middleware.BodyLimit("12M"))

	// LAN clients authenticate by source IP alone, so a browser on the LAN
	// carries that authority implicitly and any website it visits could post to
	// the dashboard. Multipart and plain forms are CORS-"simple" and trigger no
	// preflight, so cover upload and episode download were reachable that way.
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			if site := c.Request().Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				return echo.NewHTTPError(http.StatusForbidden, "Cross-site request refused")
			}
			return next(c)
		}
	})
}

// handler builds the absolute base URL used for media and artwork links in a
// generated feed. When PUBLIC_URL is configured it wins for non-local requests:
// otherwise the URLs come from a client-supplied Host header, so a forged Host
// yields a feed whose enclosures point somewhere else entirely.
func handler(r *http.Request) string {
	var scheme string
	// Honour the proxy's scheme (Cloudflare terminates TLS for us) so that
	// media and artwork URLs in an https feed are also https.
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	} else {
		scheme = "http"
	}
	if pub := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); pub != "" {
		if r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("X-Forwarded-For") != "" {
			return pub
		}
	}
	return scheme + "://" + r.Host
}

// isLocalRequest reports whether a request came directly from the local
// network (not through Cloudflare or any other proxy)
func isLocalRequest(c echo.Context) bool {
	r := c.Request()
	// Anything routed through Cloudflare (or another proxy) is not local
	if r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("Cf-Ray") != "" ||
		r.Header.Get("X-Forwarded-For") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// LAN_CIDRS narrows which addresses skip authentication. Without it any
	// RFC1918 address counts, which includes every other container on the same
	// docker bridge - several of those are themselves internet-exposed, and
	// they could read the tunnel token straight out of /api/config.
	if nets := lanNets(); len(nets) > 0 {
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return ip.IsPrivate()
}

var (
	lanNetsOnce sync.Once
	lanNetsVal  []*net.IPNet
)

func lanNets() []*net.IPNet {
	lanNetsOnce.Do(func() {
		for _, c := range strings.Split(os.Getenv("LAN_CIDRS"), ",") {
			if c = strings.TrimSpace(c); c == "" {
				continue
			}
			if _, n, err := net.ParseCIDR(c); err == nil {
				lanNetsVal = append(lanNetsVal, n)
			} else {
				log.Warnf("[AUTH] Ignoring invalid LAN_CIDRS entry %q: %v", c, err)
			}
		}
	})
	return lanNetsVal
}

// requestToken accepts the token either as ?token= (what podcast clients can
// send) or as an Authorization: Bearer header, which keeps the secret out of
// proxy logs and Referer headers for anything that can set a header.
func requestToken(c echo.Context) string {
	if h := c.Request().Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return c.Request().URL.Query().Get("token")
}

// tokenMatches compares in constant time so a wrong token cannot be narrowed
// down by timing.
func tokenMatches(given string) bool {
	want := config.AppConfig.Authentication.Token
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

func checkAuthentication(c echo.Context) error {
	tokenConfigured := config.AppConfig.Authentication.Token != ""
	basicConfigured := config.AppConfig.Authentication.BasicAuth.Password != ""

	// If no authentication configured, allow through
	if !tokenConfigured && !basicConfigured {
		return nil
	}

	// Requests from the local network never need authentication
	if isLocalRequest(c) {
		return nil
	}

	// If both basic and token are configured, accept either method (Basic OR token)
	if basicConfigured && tokenConfigured {
		user, pass, ok := c.Request().BasicAuth()
		token := requestToken(c)

		basicOk := ok && pass == config.AppConfig.Authentication.BasicAuth.Password
		if config.AppConfig.Authentication.BasicAuth.Username != "" {
			basicOk = basicOk && user == config.AppConfig.Authentication.BasicAuth.Username
		}

		tokenOk := tokenMatches(token)

		if basicOk || tokenOk {
			return nil
		}

		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	if basicConfigured {
		_, pass, ok := c.Request().BasicAuth()
		if ok && pass == config.AppConfig.Authentication.BasicAuth.Password {
			return nil
		}
		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	if tokenConfigured {
		if tokenMatches(requestToken(c)) {
			return nil
		}
		return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
	}

	return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
}
