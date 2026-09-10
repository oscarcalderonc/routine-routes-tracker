package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
)

// checkStorage reports where the data actually lives, and complains loudly when
// it is somewhere that will not survive the container being replaced.
//
// Losing the database to a redeployment is silent by nature: the tracker starts
// cleanly, serves every page, and simply has nothing in it. Worse, it looks
// exactly like a tracker that was never used. So the condition is detected and
// stated at startup rather than left to be inferred from absent data.
// It returns true when the data directory will not survive the container.
func checkStorage(log *slog.Logger, dataDir, dbPath string) bool {
	_, err := os.Stat(dbPath)
	existed := err == nil

	log.Info("data directory", "path", dataDir, "database", dbPath, "database_existed", existed)

	if !inContainer() {
		return false
	}

	mounted, err := onSeparateFilesystem(dataDir)
	if err != nil {
		log.Warn("could not determine whether the data directory is a mounted volume",
			"path", dataDir, "error", err)
		return false
	}
	if mounted {
		return false
	}

	// Inside a container, a data directory on the same filesystem as the image
	// is part of the container and goes away with it.
	log.Warn("THE DATA DIRECTORY IS NOT A MOUNTED VOLUME: everything stored will be lost "+
		"when this container is replaced, which a redeployment does. Attach persistent "+
		"storage to this path",
		"path", dataDir)
	return true
}

// inContainer reports whether this process looks containerised.
//
// Each runtime leaves its own marker and they do not agree, so all the known
// ones are checked: Docker writes /.dockerenv, while podman and other
// OCI runtimes write /run/.containerenv. Missing one would silently disable the
// warning below, which is the one thing it must not do.
func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	// Falling back to the control groups of the init process catches runtimes
	// that leave no marker file at all.
	if body, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		for _, needle := range []string{"docker", "containerd", "libpod", "kubepods"} {
			if strings.Contains(string(body), needle) {
				return true
			}
		}
	}
	return false
}

// onSeparateFilesystem reports whether a path lives on a different filesystem
// from the root. A mounted volume or bind mount does; a plain directory in the
// image does not.
func onSeparateFilesystem(path string) (bool, error) {
	dev := func(p string) (uint64, error) {
		info, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, fmt.Errorf("filesystem identity unavailable for %s", p)
		}
		return uint64(st.Dev), nil
	}

	here, err := dev(path)
	if err != nil {
		return false, err
	}
	root, err := dev("/")
	if err != nil {
		return false, err
	}
	return here != root, nil
}
