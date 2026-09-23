package database

import (
	"os"
	"path"
	"testing"
	"time"

	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/models"
)

func setupTestDB(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	if config.AppConfig == nil {
		config.AppConfig = &config.Config{}
	}

	config.AppConfig.Setup.ConfigDir = tmpDir
	config.AppConfig.Setup.DbFile = path.Join(tmpDir, "test.db")
	config.AppConfig.Setup.AudioDir = path.Join(tmpDir, "audio")

	if err := os.MkdirAll(config.AppConfig.Setup.AudioDir, 0755); err != nil {
		t.Fatalf("failed to create audio dir: %v", err)
	}

	SetupDatabase()

	return tmpDir
}

func TestDeletePodcastCronJob_RemovesFileButKeepsRecord(t *testing.T) {
	tmp := setupTestDB(t)

	videoId := "video1"
	filePath := path.Join(config.AppConfig.Setup.AudioDir, videoId+".m4a")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	hist := &models.EpisodePlaybackHistory{
		YoutubeVideoId:   videoId,
		LastAccessDate:   time.Now().Add(-8 * 24 * time.Hour).Unix(),
		TotalTimeSkipped: 0,
		Served:           true,
	}
	if err := db.Create(hist).Error; err != nil {
		t.Fatalf("failed to create history: %v", err)
	}

	DeletePodcastCronJob(nil)

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be removed, stat error: %v", err)
	}

	// The row must SURVIVE: it carries `served`, which is what stops the auto
	// downloader immediately fetching the episode again.
	var out models.EpisodePlaybackHistory
	if err := db.Where("youtube_video_id = ?", videoId).First(&out).Error; err != nil {
		t.Fatalf("expected DB record to be kept, got: %v", err)
	}
	if !out.Served {
		t.Fatalf("expected served to stay true")
	}

	_ = tmp
}

// An episode nobody has fetched must never be aged out by this job.
func TestDeletePodcastCronJob_KeepsUnservedEpisode(t *testing.T) {
	setupTestDB(t)

	videoId := "video-unserved"
	filePath := path.Join(config.AppConfig.Setup.AudioDir, videoId+".m4a")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := db.Create(&models.EpisodePlaybackHistory{
		YoutubeVideoId: videoId,
		LastAccessDate: time.Now().Add(-8 * 24 * time.Hour).Unix(),
		Served:         false,
	}).Error; err != nil {
		t.Fatalf("failed to create history: %v", err)
	}

	DeletePodcastCronJob(nil)

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected un-served episode to be kept, stat error: %v", err)
	}
}

// A download in flight must not have its file pulled out from under it.
func TestDeletePodcastCronJob_SkipsBusyEpisode(t *testing.T) {
	setupTestDB(t)

	videoId := "video-busy"
	filePath := path.Join(config.AppConfig.Setup.AudioDir, videoId+".m4a")
	if err := os.WriteFile(filePath, []byte("data"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := db.Create(&models.EpisodePlaybackHistory{
		YoutubeVideoId: videoId,
		LastAccessDate: time.Now().Add(-8 * 24 * time.Hour).Unix(),
		Served:         true,
	}).Error; err != nil {
		t.Fatalf("failed to create history: %v", err)
	}

	DeletePodcastCronJob(func(id string) bool { return id == videoId })

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected busy episode to be kept, stat error: %v", err)
	}
}

func TestDeletePodcastCronJob_KeepsRecordWhenFileMissing(t *testing.T) {
	setupTestDB(t)

	videoId := "video-missing"
	hist := &models.EpisodePlaybackHistory{
		YoutubeVideoId:   videoId,
		LastAccessDate:   time.Now().Add(-8 * 24 * time.Hour).Unix(),
		TotalTimeSkipped: 0,
		Served:           true,
	}
	if err := db.Create(hist).Error; err != nil {
		t.Fatalf("failed to create history: %v", err)
	}

	DeletePodcastCronJob(nil)

	// Row kept even when the file was already gone — the served flag is the
	// point of the row, not the file.
	var out models.EpisodePlaybackHistory
	if err := db.Where("youtube_video_id = ?", videoId).First(&out).Error; err != nil {
		t.Fatalf("expected DB record to be kept, got: %v", err)
	}
}
