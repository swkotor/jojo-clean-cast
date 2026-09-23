// Package rssfeed pulls episodes from a normal podcast RSS feed, as an
// alternative to YouTube.
//
// It exists so shows that are NOT on YouTube (or whose YouTube upload is worse)
// can still be de-added. The ad removal itself is in the adstrip package and
// relies on the host stitching ads per download; this package only resolves and
// parses the feed.
package rssfeed

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ikoyhn/podcast-sponsorblock/internal/models"
)

var client = &http.Client{
	Timeout: 45 * time.Second,
	// Apple share links and lnk.to smart links are several redirects deep.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 12 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

const userAgent = "jojo-podcasts/1.0 (+https://github.com/swkotor/jojo-clean-cast)"

// EpisodeKey is the synthetic, stable episode id used in place of a YouTube
// video id. It must survive IsValidID (letters, digits, '_' and '-' only), so
// the existing media routes, file names and playback history all keep working.
func EpisodeKey(guid string) string {
	sum := sha1.Sum([]byte(guid))
	return "rss_" + hex.EncodeToString(sum[:])[:16]
}

// PodcastKey is the synthetic podcast id for a feed with no YouTube identity.
func PodcastKey(feedURL string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(feedURL)))
	return "rssp_" + hex.EncodeToString(sum[:])[:16]
}

var appleIDRe = regexp.MustCompile(`/id(\d+)`)

// Resolve turns whatever the user pasted into an actual feed URL.
//
// Accepts a direct feed, an Apple Podcasts page (looked up through the iTunes
// API, which reports the real feedUrl) or a redirector such as lnk.to, which is
// followed and then re-examined — a smart link usually lands on Apple.
func Resolve(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("no url given")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}

	if feed, ok, err := appleFeed(raw); err != nil {
		return "", err
	} else if ok {
		return feed, nil
	}

	// Not Apple: fetch it and decide from what comes back.
	body, finalURL, ctype, err := get(raw, 512*1024)
	if err != nil {
		return "", err
	}
	// A redirector may have landed us on Apple after all.
	if feed, ok, err := appleFeed(finalURL); err == nil && ok {
		return feed, nil
	}
	if looksLikeFeed(ctype, body) {
		return finalURL, nil
	}
	// An HTML page: use its advertised RSS alternate, if any.
	if href := feedLinkFromHTML(string(body), finalURL); href != "" {
		return href, nil
	}
	return "", fmt.Errorf("could not find a podcast feed at %s", raw)
}

// appleFeed maps an Apple Podcasts URL to its feed via the iTunes lookup API.
func appleFeed(raw string) (string, bool, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(u.Host, "apple.com") {
		return "", false, nil
	}
	m := appleIDRe.FindStringSubmatch(u.Path)
	if m == nil {
		if id := u.Query().Get("i"); id != "" {
			m = []string{"", id}
		} else {
			return "", false, nil
		}
	}
	body, _, _, err := get("https://itunes.apple.com/lookup?entity=podcast&id="+m[1], 256*1024)
	if err != nil {
		return "", false, err
	}
	var out struct {
		Results []struct {
			FeedURL string `json:"feedUrl"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false, fmt.Errorf("apple lookup returned unexpected data: %w", err)
	}
	if len(out.Results) == 0 || out.Results[0].FeedURL == "" {
		return "", false, fmt.Errorf("apple has no feed url for that show (it may be Apple-exclusive)")
	}
	return out.Results[0].FeedURL, true, nil
}

func looksLikeFeed(ctype string, body []byte) bool {
	if strings.Contains(ctype, "xml") || strings.Contains(ctype, "rss") {
		return true
	}
	head := strings.ToLower(string(body[:min(len(body), 1024)]))
	return strings.Contains(head, "<rss") || strings.Contains(head, "<feed")
}

var linkRe = regexp.MustCompile(`(?is)<link[^>]+>`)
var hrefRe = regexp.MustCompile(`(?is)href\s*=\s*["']([^"']+)["']`)

func feedLinkFromHTML(html, base string) string {
	for _, tag := range linkRe.FindAllString(html, -1) {
		low := strings.ToLower(tag)
		if !strings.Contains(low, "alternate") {
			continue
		}
		if !strings.Contains(low, "rss+xml") && !strings.Contains(low, "atom+xml") {
			continue
		}
		if m := hrefRe.FindStringSubmatch(tag); m != nil {
			if u, err := url.Parse(m[1]); err == nil {
				if b, err := url.Parse(base); err == nil {
					return b.ResolveReference(u).String()
				}
			}
		}
	}
	return ""
}

func get(u string, limit int64) ([]byte, string, string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, u)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, "", "", err
	}
	return body, resp.Request.URL.String(), resp.Header.Get("Content-Type"), nil
}

// ---- feed parsing ----

type rss struct {
	Channel struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		Language    string `xml:"language"`
		Author      string `xml:"author"`
		Explicit    string `xml:"explicit"`
		Image       struct {
			Href string `xml:"href,attr"`
			URL  string `xml:"url"`
		} `xml:"image"`
		Categories []struct {
			Text string `xml:"text,attr"`
		} `xml:"category"`
		Items []item `xml:"item"`
	} `xml:"channel"`
}

type item struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	Summary     string `xml:"summary"`
	PubDate     string `xml:"pubDate"`
	Guid        string `xml:"guid"`
	Duration    string `xml:"duration"`
	Image       struct {
		Href string `xml:"href,attr"`
	} `xml:"image"`
	Enclosure struct {
		URL  string `xml:"url,attr"`
		Type string `xml:"type,attr"`
	} `xml:"enclosure"`
}

// Feed is a parsed podcast feed.
type Feed struct {
	Title       string
	Description string
	Author      string
	ImageURL    string
	Category    string
	Explicit    string
	Episodes    []models.PodcastEpisode
}

var dateFormats = []string{
	time.RFC1123Z, time.RFC1123,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	"2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05",
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, f := range dateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// parseDuration accepts the iTunes forms: seconds, M:SS or H:MM:SS.
func parseDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if !strings.Contains(s, ":") {
		if n, err := strconv.Atoi(s); err == nil {
			return time.Duration(n) * time.Second
		}
		return 0
	}
	var total int
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return time.Duration(total) * time.Second
}

// Fetch downloads and parses a feed.
func Fetch(feedURL, podcastID string) (*Feed, error) {
	body, _, _, err := get(feedURL, 16*1024*1024)
	if err != nil {
		return nil, err
	}
	var r rss
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = false
	// Feeds in the wild declare all sorts of encodings; pass bytes through
	// rather than refusing to parse.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("could not parse feed: %w", err)
	}
	ch := r.Channel

	f := &Feed{
		Title:       strings.TrimSpace(ch.Title),
		Description: strings.TrimSpace(ch.Description),
		Author:      strings.TrimSpace(ch.Author),
		Explicit:    strings.TrimSpace(ch.Explicit),
	}
	f.ImageURL = ch.Image.Href
	if f.ImageURL == "" {
		f.ImageURL = ch.Image.URL
	}
	if len(ch.Categories) > 0 {
		f.Category = ch.Categories[0].Text
	}

	for _, it := range ch.Items {
		if it.Enclosure.URL == "" {
			continue // no audio: a text-only entry
		}
		guid := strings.TrimSpace(it.Guid)
		if guid == "" {
			guid = it.Enclosure.URL // feeds without a guid: the url is stable enough
		}
		desc := it.Description
		if strings.TrimSpace(desc) == "" {
			desc = it.Summary
		}
		f.Episodes = append(f.Episodes, models.PodcastEpisode{
			YoutubeVideoId:     EpisodeKey(guid),
			Guid:               guid,
			EnclosureUrl:       it.Enclosure.URL,
			EpisodeName:        strings.TrimSpace(it.Title),
			EpisodeDescription: strings.TrimSpace(desc),
			PublishedDate:      parseDate(it.PubDate),
			Duration:           parseDuration(it.Duration),
			ImageUrl:           it.Image.Href,
			PodcastId:          podcastID,
			Type:               "RSS",
		})
	}
	return f, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
