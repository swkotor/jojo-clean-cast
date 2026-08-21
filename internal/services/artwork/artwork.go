package artwork

import (
	"context"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	log "github.com/labstack/gommon/log"
	"github.com/lrstanley/go-ytdlp"
)

var channelIdRegex = regexp.MustCompile(`^UC[A-Za-z0-9_-]{20,}$`)

// Dir returns the directory auto-fetched artwork is cached in
func Dir() string {
	return filepath.Join(config.AppConfig.Setup.ConfigDir, "art")
}

type ytThumb struct {
	Url    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type ytPlaylistInfo struct {
	Title      string    `json:"title"`
	Thumbnails []ytThumb `json:"thumbnails"`
}

// playlistArtUrl asks yt-dlp for a playlist's own square "podcast" artwork
// (i.ytimg.com/pl_c/<id>/studio_square_thumbnail.jpg), which the YouTube Data
// API does not expose — it only returns the latest video's thumbnail.
func playlistArtUrl(playlistId string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := ytdlp.New().
		FlatPlaylist().
		PlaylistItems("0").
		DumpSingleJSON().
		NoWarnings().
		Run(ctx, "https://www.youtube.com/playlist?list="+playlistId)
	if err != nil || res == nil || strings.TrimSpace(res.Stdout) == "" {
		return ""
	}
	var info ytPlaylistInfo
	if err := json.Unmarshal([]byte(res.Stdout), &info); err != nil {
		return ""
	}
	best, bestArea := "", 0
	for _, t := range info.Thumbnails {
		// prefer the square studio thumbnail, largest available
		if !strings.Contains(t.Url, "studio_square_thumbnail") {
			continue
		}
		if a := t.Width * t.Height; a >= bestArea {
			best, bestArea = t.Url, a
		}
	}
	if best == "" {
		// fall back to the largest square-ish thumbnail offered
		for _, t := range info.Thumbnails {
			if t.Width > 0 && t.Height > 0 && t.Width == t.Height {
				if a := t.Width * t.Height; a >= bestArea {
					best, bestArea = t.Url, a
				}
			}
		}
	}
	return best
}

// download stores an image locally and returns the saved filename
func download(podcastId, imageUrl string) (string, error) {
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Get(imageUrl)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", os.ErrNotExist
	}
	ext := ".jpg"
	switch resp.Header.Get("Content-Type") {
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	// remove any previously cached art for this podcast
	if old := database.FindFileWithId(Dir(), podcastId); old != "" {
		os.Remove(old)
	}
	name := podcastId + ext
	f, err := os.Create(filepath.Join(Dir(), name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	f.Close()

	// Podcast clients (Apple in particular) expect square artwork. If we had
	// to fall back to a 16:9 video thumbnail, centre-crop it to a square.
	if squareName, err := makeSquare(filepath.Join(Dir(), name)); err == nil && squareName != "" {
		name = squareName
	}
	return name, nil
}

// makeSquare centre-crops a non-square image and rewrites it as JPEG.
// Returns the (possibly new) filename, or "" if nothing needed doing.
func makeSquare(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(in)
	in.Close()
	if err != nil {
		return "", err // e.g. webp — leave it as-is
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == h {
		return "", nil
	}
	side := w
	if h < w {
		side = h
	}
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	out := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(out, out.Bounds(), img, image.Point{X: x0, Y: y0}, draw.Src)

	newPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".jpg"
	f, err := os.Create(newPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := jpeg.Encode(f, out, &jpeg.Options{Quality: 90}); err != nil {
		return "", err
	}
	if newPath != path {
		os.Remove(path)
	}
	return filepath.Base(newPath), nil
}

// Resolve fetches and caches the best artwork for a feed. Playlists use their
// own square podcast art; channels use the channel avatar at 1400px.
func Resolve(podcastId string) {
	p := database.GetPodcast(podcastId)
	if p == nil || p.IsVirtual() {
		return
	}

	imageUrl := ""
	if channelIdRegex.MatchString(podcastId) {
		imageUrl = squareChannelAvatar(p.ImageUrl)
	} else {
		imageUrl = playlistArtUrl(podcastId)
		if imageUrl == "" {
			imageUrl = p.ImageUrl // fall back to whatever the API gave us
		}
	}
	if imageUrl == "" {
		return
	}

	name, err := download(podcastId, imageUrl)
	if err != nil {
		log.Warnf("[ARTWORK] Could not cache artwork for %s: %v", podcastId, err)
		return
	}
	if err := database.SetAutoImage(podcastId, name); err != nil {
		log.Errorf("[ARTWORK] %v", err)
		return
	}
	log.Infof("[ARTWORK] Cached artwork for %s", p.DisplayName())
}

// squareChannelAvatar bumps a yt3.ggpht avatar URL to a large square size
func squareChannelAvatar(u string) string {
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "=s"); i > 0 {
		return u[:i] + "=s1400-c-k-c0x00ffffff-no-rj"
	}
	return u
}

// BackfillAll caches artwork for every feed that has none yet
func BackfillAll() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				events.Error("Artwork backfill crashed: %v", r)
			}
		}()
		time.Sleep(20 * time.Second)
		podcasts, err := database.GetAllPodcasts()
		if err != nil {
			return
		}
		done := 0
		for _, p := range podcasts {
			if p.IsVirtual() || p.AutoImage != "" {
				continue
			}
			Resolve(p.Id)
			done++
			time.Sleep(500 * time.Millisecond)
		}
		if done > 0 {
			events.Info("Cached podcast artwork for %d feed(s)", done)
		}
	}()
}
