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
	// Addr is the listen address, for example ":8381".
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
	// DriveFolderID identifies the Google Drive folder to read recordings from.
	// It is the last path element of the folder's address in Drive. Leaving it
	// empty disables cloud access; recordings placed in the inbox by any other
	// means are still imported.
	DriveFolderID string
	// GoogleCredentialsB64 is a service account key encoded as base64, which is
	// how a key is most conveniently carried in an environment variable.
	GoogleCredentialsB64 string
	// GoogleCredentialsFile is a path to a service account key, as an
	// alternative to supplying it inline.
	GoogleCredentialsFile string
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
		Addr:                  env("ADDR", ":"+env("PORT", "8381")),
		DataDir:               dataDir,
		DBPath:                env("DB_PATH", filepath.Join(dataDir, "tracker.db")),
		Location:              loc,
		Anchor:                anchor,
		DriveFolderID:         env("DRIVE_FOLDER_ID", ""),
		GoogleCredentialsB64:  os.Getenv("GOOGLE_CREDENTIALS_B64"),
		GoogleCredentialsFile: env("GOOGLE_CREDENTIALS_FILE", ""),
		MaxUploadBytes:        maxUpload,
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
