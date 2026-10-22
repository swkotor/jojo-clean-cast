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
  YouTube 403 breakage, upstream issue #112). The updater runs in its own
  process group and logs a marker before it starts (the app used to die
  silently, exit 0, at exactly the daily-update tick).
- **yt-dlp JS runtime**: image ships `nodejs` and downloads pass
  `--js-runtimes node` — without a JS runtime yt-dlp's deprecated no-JS
  YouTube path intermittently got 403 Forbidden.
- **On-demand download failures now retried**: `/media` only touches playback
  history after a successful serve (a failed download used to create the row,
  which auto-download read as "already served — skip forever"), and failures
  feed the same backoff/badge tracking as the poller.
- **Restart-proof "served" markers**: startup file-tracking no longer deletes
  playback-history rows of cleaned-up known episodes (upstream's delete
  caused a full re-download of every feed's recent episodes after each
  restart).
- **ISO-8601 durations parsed properly** (`P0D` premieres/lives skipped
  quietly instead of erroring on every refresh).
- **SponsorBlock 404s cached 6h** (the re-cut pass was polling every episode
  every 30 min and spamming "Video not found").
- **Per-podcast storage** in `AUDIO_DIR/<podcast title>/`
- `/healthz` + Docker HEALTHCHECK

### Ad removal (three layers, each covering what the others cannot)

1. **SponsorBlock** — community-marked segments on the YouTube source.
2. **Differential stripping** (`internal/services/adstrip/`) — downloads an
   episode 3x (`AD_STRIP_VARIANTS`) and keeps only the MPEG frames present in
   every copy. Hosts stitch ads per-listener, so the show frames are
   byte-identical across downloads and only the ads differ. Measured 98.6-99.0%
   of frames shared. RSS episodes only; costs the extra downloads.
3. **Host-read removal** (`internal/services/adtext/`, opt-in per podcast via
   the "Host ads" chip) — transcribes the episode with whisper.cpp and cuts what
   the hosts read aloud, which is baked into the master and so invisible to both
   layers above. Two detectors:
   - *sponsor scoring* (`adtext.go`): a segment needs BOTH a sponsor noun and a
     call to action; tuned and tested against a real VGC episode.
   - *break hand-off* (`breaks.go`): an opening cue, a closing cue within 90s,
     and an ad mention in between. Giant Bomb's shows do not read sponsor copy,
     they announce the break — and the inserted ad is already gone by then, so
     what is left is the announcement for an ad that is not there.

   Cuts are lossless: MP3 by dropping frames, AAC/MP4 by an ffmpeg stream copy
   that rebuilds the sample tables (plus `+faststart`, which Apple Podcasts
   needs). Results are duration-checked before replacing the episode, and the
   stored duration follows the shorter file.

   - *brand repetition* (`brand.go`): the advertiser's name repeated inside one
     short stretch and absent from the rest of the episode, next to its own
     address. Finds ads performed as a bit, which carry no sponsor vocabulary at
     all — a 75s Merchantech spot on Game Mess Mornings was invisible to
     everything else. The address check is what makes it safe: without it the
     detector fired on any subject discussed in a burst.

   Signals are matched in a 10s WINDOW (whisper's segmentation varies hugely —
   958 segments vs 313 for the same audio depending on the engine), but whether
   a segment BELONGS to a read is judged on its own text; using the window for
   both made every segment beside an ad look like one. Joining is time-aware:
   transcript segments skip silence, so index-adjacent can be minutes apart.

   `adtext.RulesRevision` is recorded per episode, so improving the rules
   re-opens the episodes scanned under the weaker ones.

   **Transcription runs on the GPU sidecar** (`jojo-transcriber`, port 9000,
   `deploy-transcriber.sh`, `TRANSCRIBER_URL`). It is optional — the app falls
   back to the bundled whisper.cpp — but whisper.cpp on CPU does not merely run
   slower, it LOSES speech: across the 50s holding a Kinda Funny membership
   pitch it produced the single line "Are you an influencer?" where
   faster-whisper produced eleven accurate ones. Detection cannot find an ad
   that is missing from the transcript. GPU is 65-105x realtime against ~5x.

   Transcription is serialised process-wide and runs one episode per poll
   (`DrainHostAdBacklog`), idle-gated, with `podcast_episodes.ad_text_done` so a
   backlog is resumable and a clean episode is never transcribed twice. Budget
   roughly realtime/5 of CPU per episode on a busy box.

   **Known limits**: whisper segments are several seconds long, so a cut takes a
   second or two of neighbouring speech. And it does not touch a pitch that is
   interleaved with the conversation sentence by sentence — the Giant Bomb shows'
   opening premium plug is improvised banter wrapped around real content, and
   nothing span-based can remove it without taking the show too.

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
