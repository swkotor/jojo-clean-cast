# jojo-clean-cast — fork notes

Fork of [ikoyhn/clean-cast](https://github.com/ikoyhn/clean-cast) with a web
dashboard and automation features, deployed on Unraid as `jojo-podcasts`.

## Added features

- **Web dashboard** at `/` — add podcasts (URL/ID/@handle), rename feeds,
  custom cover art, per-episode download/delete, activity/error log,
  subscribed marker, YouTube source links, stats header
- **Auto-download** of new episodes (poller, per-podcast toggle) with retry
  backoff and failure badges
- **Cleanup**: served episodes deleted after `AUTO_CLEANUP_AFTER` (default 6h);
  only the latest 5 episodes per feed are kept on disk; optional global
  `MAX_STORAGE_GB` cap (oldest deleted first). Deleted episodes re-download
  on demand.
- **Filtered sub-feeds** ("split"): virtual podcasts with id
  `<parentId>~<slug>` that republish a title-filtered subset of a parent feed
- **Playlist browser**: paste a channel /playlists URL to list and subscribe
  to individual playlists
- **OPML export** at `/opml`
- **Auth**: LAN requests skip token auth; proxied (Cloudflare) requests
  require `?token=`
- **yt-dlp self-update** to nightly at startup and daily (fixes recurring
  YouTube 403 breakage, upstream issue #112)
- **Per-podcast storage** in `AUDIO_DIR/<podcast title>/`
- `/healthz` + Docker HEALTHCHECK

## Environment variables (added)

| Var | Default | Purpose |
|---|---|---|
| `AUDIO_DIR` | `<config>/audio` | Audio storage root (per-podcast subfolders) |
| `PUBLIC_URL` | — | Tunnel URL for remote subscribe links |
| `AUTO_DOWNLOAD` | on | `off` disables the poller |
| `AUTO_DOWNLOAD_INTERVAL` | `30m` | Poll frequency |
| `AUTO_CLEANUP_AFTER` | `6h` | Delete served episodes after this idle time (`off` disables) |
| `MAX_STORAGE_GB` | — | Global storage cap |

## Files added by this fork (safe from upstream merges)

- `internal/app/dashboard.go`, `internal/app/web/index.html`
- `internal/services/autodl/`, `internal/services/events/`,
  `internal/services/filterfeed/`
- `internal/database/dashboardRepository.go`
- `FORK_NOTES.md`

## Upstream files we touch (merge-conflict candidates)

- `internal/app/controller.go` — `registerDashboardRoutes()` call, feed-fetch
  tracking, virtual-feed branch in `/rss`, `isLocalRequest()` auth bypass
- `internal/app/app.go` — `autodl.Start()`
- `internal/models/podcast.go` — added Podcast columns + helpers
- `internal/services/rss/rssService.go` — `DisplayName()`, custom cover URL
- `internal/services/downloader/ytdlpService.go` — per-podcast dir, SB
  override, remote components, event logging
- `internal/database/episodeRepository.go` — recursive file lookup
- `internal/database/playbackHistoryRepository.go` — recursive file listing
- `internal/config/config.go` — honor `AUDIO_DIR`
- `Dockerfile` — HEALTHCHECK
- `go.mod`/`go.sum` — go-ytdlp v1.3.6

## Auto-update pipeline (on Unraid)

`/mnt/user/appdata/jojo-podcasts/update.sh` runs weekly via cron: fetches
upstream, merges into a temp branch, builds; only if the merge is clean AND
the build passes does it push to the fork and redeploy. On conflict or build
failure it aborts, leaves the running container untouched, and raises an
Unraid notification (that's when to bring in Claude).
