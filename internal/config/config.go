// Package config reads the tracker's settings from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/oscarcalderonc/routine-routes-tracker/internal/matcher"
)

// Config holds every setting the tracker needs. Values come from the
// environment so that deployments can supply them without a config file.
type Config struct {
	// Addr is the listen address, for example ":8080".
	Addr string
	// DataDir holds the database, the retained source files and the sync inbox.
	DataDir string
	// DBPath is the SQLite database file.
	DBPath string
	// Location is the timezone in which dates and hours are reported. Local
	// dates are derived at ingest using this zone rather than the container's,
	// so that changing the host has no effect on stored history.
	Location *time.Location
	// Anchor selects which instant of a waypoint pass bounds a segment.
	Anchor matcher.Anchor
	// RcloneBin is the rclone executable used to pull new files.
	RcloneBin string
	// RcloneConfig is the path to an rclone configuration file, written from a
	// secret at startup when RcloneConfigB64 is set.
	RcloneConfig string
	// RcloneConfigB64 is a base64-encoded rclone configuration supplied as a
	// secret.
	RcloneConfigB64 string
	// DriveRemote and DriveFolder identify the folder to pull from, as in
	// "gdrive" and "gpx".
	DriveRemote string
	DriveFolder string
	// MaxUploadBytes caps the size of a single source file.
	MaxUploadBytes int64
}

// Load reads the configuration, applying defaults for everything that is not
// required. It returns an error only when a supplied value cannot be used.
func Load() (Config, error) {
	dataDir := env("DATA_DIR", "./data")

	tzName := env("APP_TZ", "UTC")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return Config{}, fmt.Errorf("load timezone %q: %w", tzName, err)
	}

	anchor := matcher.Anchor(env("ANCHOR", string(matcher.AnchorEntry)))
	if anchor != matcher.AnchorEntry && anchor != matcher.AnchorClosest {
		return Config{}, fmt.Errorf("ANCHOR must be %q or %q, got %q",
			matcher.AnchorEntry, matcher.AnchorClosest, anchor)
	}

	maxUpload, err := strconv.ParseInt(env("MAX_UPLOAD_BYTES", "26214400"), 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("parse MAX_UPLOAD_BYTES: %w", err)
	}

	return Config{
		Addr:            env("ADDR", ":"+env("PORT", "8080")),
		DataDir:         dataDir,
		DBPath:          env("DB_PATH", filepath.Join(dataDir, "tracker.db")),
		Location:        loc,
		Anchor:          anchor,
		RcloneBin:       env("RCLONE_BIN", "rclone"),
		RcloneConfig:    env("RCLONE_CONFIG", filepath.Join(dataDir, "rclone.conf")),
		RcloneConfigB64: os.Getenv("RCLONE_CONFIG_B64"),
		DriveRemote:     env("DRIVE_REMOTE", ""),
		DriveFolder:     env("DRIVE_FOLDER", ""),
		MaxUploadBytes:  maxUpload,
	}, nil
}

// InboxDir is the directory that source files are synced into.
func (c Config) InboxDir() string { return filepath.Join(c.DataDir, "inbox") }

// BlobDir is the directory that retained source files live in.
func (c Config) BlobDir() string { return filepath.Join(c.DataDir, "gpx") }

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
