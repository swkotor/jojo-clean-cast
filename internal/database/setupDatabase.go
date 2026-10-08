package database

import (
	log "github.com/labstack/gommon/log"
	"ikoyhn/podcast-sponsorblock/internal/config"
	"ikoyhn/podcast-sponsorblock/internal/models"
	"os"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var db *gorm.DB

func SetupDatabase() {
	var err error
	// Create the database file if it doesn't exist
	if _, err := os.Stat(config.AppConfig.Setup.DbFile); os.IsNotExist(err) {
		err := os.MkdirAll(config.AppConfig.Setup.ConfigDir, os.ModePerm)
		if err != nil {
			panic(err)
		}
		f, err := os.Create(config.AppConfig.Setup.DbFile)
		if err != nil {
			panic(err)
		}
		err = f.Close()
		if err != nil {
			return
		}
	}

	db, err = gorm.Open(sqlite.Open(config.AppConfig.Setup.DbFile), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		panic(err)
	}

	// WAL lets the 30-minute writer and a multi-second feed read proceed at
	// once. With the default rollback journal they serialise, which is how a
	// growing library starts returning "database is locked" under a feed
	// refresh. busy_timeout turns any remaining contention into a short wait
	// instead of an immediate error.
	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		log.Warn("[DB] Could not enable WAL: " + err.Error())
	}
	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		log.Warn("[DB] Could not set busy_timeout: " + err.Error())
	}

	err = db.AutoMigrate(&models.EpisodePlaybackHistory{})
	if err != nil {
		panic(err)
	}
	err = db.AutoMigrate(&models.PodcastEpisode{})
	if err != nil {
		panic(err)
	}
	err = db.AutoMigrate(&models.Podcast{})
	if err != nil {
		panic(err)
	}
}
