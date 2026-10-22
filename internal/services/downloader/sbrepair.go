package downloader

// Repairing a download where yt-dlp declined to apply SponsorBlock.

import (
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/database"
	"ikoyhn/podcast-sponsorblock/internal/services/adtext"
	"ikoyhn/podcast-sponsorblock/internal/services/events"
	"ikoyhn/podcast-sponsorblock/internal/services/sponsorblock"
	"time"

	log "github.com/labstack/gommon/log"
)

// refuse to remove more than this share of an episode, however confident the
// signals are — a wrong cut of that size is worse than the ads
const maxSbRepairFraction = 0.25

// VerifySponsorBlock checks that the segments SponsorBlock offered were really
// removed from the finished file, records the TRUE baseline, and cuts them
// ourselves when yt-dlp refused.
//
// Called after the file is in place, so every outcome leaves a playable
// episode. Returns the number of seconds actually missing from the file.
func VerifySponsorBlock(videoId, title, file string) float64 {
	segs, videoDur, err := sponsorblock.Fetch(videoId)
	if err != nil {
		log.Errorf("[SB] %s: could not fetch segments: %v", videoId, err)
		return 0
	}
	if len(segs) == 0 {
		return 0
	}
	var offered float64
	for _, s := range segs {
		offered += s.End - s.Start
	}
	fileDur, err := adtext.ProbeDuration(file)
	if err != nil {
		log.Errorf("[SB] %s: %v", videoId, err)
		return 0
	}

	outcome, removed := sponsorblock.Removed(fileDur, videoDur, offered, len(segs))
	switch outcome {
	case sponsorblock.Applied:
		log.Infof("[SB] %s: %.0fs removed by yt-dlp across %d segment(s)", videoId, removed, len(segs))
		return removed
	case sponsorblock.Unclear:
		// Length matches neither the full video nor the trimmed one. Report
		// what is missing and leave the audio alone: we cannot tell which
		// segments, if any, are still in there.
		log.Warnf("[SB] %s: file %.0fs, video %.0fs, offered %.0fs — cannot tell what was removed; leaving it",
			videoId, fileDur, videoDur, offered)
		return removed
	}

	// NotApplied: yt-dlp kept the whole thing. Almost always the duration
	// disagreement — it will not cut on timestamps it cannot vouch for.
	events.Info("SponsorBlock offered %.0fs for %s but yt-dlp removed nothing; checking the transcript", offered, title)
	log.Warnf("[SB] %s: %.0fs offered, nothing removed (file %.0fs vs video %.0fs) — attempting repair",
		videoId, offered, fileDur, videoDur)

	cfg := config.AppConfig.Setup.ConfigDir
	if !adtext.Available(cfg) {
		if err := adtext.EnsureModel(cfg); err != nil {
			events.Error("Cannot repair %s: %v", title, err)
			return 0
		}
	}
	started := time.Now()
	tsegs, err := adtext.Transcribe(cfg, file, 8)
	if err != nil {
		events.Error("Cannot repair %s: %v", title, err)
		return 0
	}
	spans := adtext.ConfirmSponsorSpans(tsegs, sponsorblock.Hints(segs), fileDur)
	if len(spans) == 0 {
		events.Error("SponsorBlock marked %s but the transcript shows no ads there; nothing cut", title)
		log.Warnf("[SB] %s: no marked window reads as advertising (%s)", videoId, sponsorblock.Describe(segs))
		return 0
	}
	var want float64
	for _, s := range spans {
		want += s.End - s.Start
	}
	if want > maxSbRepairFraction*fileDur {
		events.Error("Refusing to cut %.0fs of a %.0fs episode (%s)", want, fileDur, title)
		return 0
	}

	cut, err := adtext.CutStreamCopy(file, spans, fileDur)
	if err != nil {
		events.Error("Could not cut ads from %s: %v", title, err)
		return 0
	}
	if d, err := adtext.ProbeDuration(file); err == nil && d > 0 {
		database.UpdateEpisodeDuration(videoId, int(d))
	}
	events.Info("Removed %.0fs of ads from %s that yt-dlp had left in (%s)",
		cut, title, adtext.Describe(spans))
	log.Infof("[SB] %s: repaired, cut %.0fs in %d span(s) in %s — offset %.0fs",
		videoId, cut, len(spans), time.Since(started).Round(time.Second),
		adtext.Offset(tsegs, sponsorblock.Hints(segs)))
	return cut
}
