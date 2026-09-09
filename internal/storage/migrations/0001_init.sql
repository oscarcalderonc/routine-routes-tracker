-- Initial schema.
--
-- Times are stored as RFC 3339 strings in UTC, which sort correctly as text.
-- Local dates and hours are denormalised at ingest so that reporting never has
-- to reinterpret a UTC instant in a timezone, which would misbucket journeys
-- either side of midnight and break across daylight saving transitions.

CREATE TABLE route_templates (
    id         TEXT PRIMARY KEY,
    name       TEXT    NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1,
    -- Incremented on every waypoint change; trips recorded against an older
    -- version are recomputed automatically.
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE waypoints (
    id          TEXT PRIMARY KEY,
    template_id TEXT    NOT NULL REFERENCES route_templates(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    label       TEXT    NOT NULL,
    lat         REAL    NOT NULL,
    lon         REAL    NOT NULL,
    radius_m    REAL    NOT NULL DEFAULT 25,
    optional    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    UNIQUE (template_id, seq)
);

CREATE TABLE trips (
    id                TEXT PRIMARY KEY,
    template_id       TEXT REFERENCES route_templates(id) ON DELETE CASCADE,
    template_version  INTEGER,
    algo_version      INTEGER,
    source_filename   TEXT NOT NULL,
    content_sha256    TEXT NOT NULL,
    started_at        TEXT,
    ended_at          TEXT,
    local_date        TEXT,
    local_weekday     INTEGER,
    local_hour        INTEGER,
    direction         TEXT,
    point_count       INTEGER NOT NULL DEFAULT 0,
    kept_point_count  INTEGER NOT NULL DEFAULT 0,
    duration_s        REAL,
    distance_m        REAL,
    min_lat           REAL,
    min_lon           REAL,
    max_lat           REAL,
    max_lon           REAL,
    status            TEXT NOT NULL,
    matched_waypoints INTEGER NOT NULL DEFAULT 0,
    match_score       REAL,
    error_message     TEXT,
    processed_at      TEXT NOT NULL
);

CREATE INDEX trips_template_started ON trips (template_id, started_at DESC);
CREATE INDEX trips_status           ON trips (status);
CREATE INDEX trips_local_date       ON trips (local_date);

-- The simplified path shown on the map. The authoritative record is always the
-- retained source file; this is a derived payload that may be rebuilt.
CREATE TABLE trip_tracks (
    trip_id     TEXT PRIMARY KEY REFERENCES trips(id) ON DELETE CASCADE,
    encoding    TEXT    NOT NULL,
    point_count INTEGER NOT NULL,
    payload     BLOB    NOT NULL
);

CREATE TABLE crossings (
    id                 TEXT PRIMARY KEY,
    trip_id            TEXT NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    waypoint_id        TEXT NOT NULL REFERENCES waypoints(id) ON DELETE CASCADE,
    waypoint_seq       INTEGER NOT NULL,
    crossed_at         TEXT NOT NULL,
    offset_s           REAL NOT NULL,
    exit_at            TEXT,
    closest_at         TEXT,
    closest_distance_m REAL,
    method             TEXT,
    enter_index        INTEGER,
    UNIQUE (trip_id, waypoint_id)
);

CREATE INDEX crossings_waypoint_time ON crossings (waypoint_id, crossed_at);

-- Deliberately denormalised: this is the only table the statistics pages read.
-- seq is always in canonical template order, so a given value denotes the same
-- stretch of road whichever direction it was driven, and the two directions
-- pool together unless the reader asks to separate them.
CREATE TABLE segments (
    id               TEXT PRIMARY KEY,
    trip_id          TEXT NOT NULL REFERENCES trips(id) ON DELETE CASCADE,
    template_id      TEXT NOT NULL REFERENCES route_templates(id) ON DELETE CASCADE,
    seq              INTEGER NOT NULL,
    from_waypoint_id TEXT NOT NULL,
    to_waypoint_id   TEXT NOT NULL,
    label            TEXT NOT NULL,
    direction        TEXT,
    started_at       TEXT,
    ended_at         TEXT,
    local_date       TEXT,
    local_weekday    INTEGER,
    local_hour       INTEGER,
    -- NULL where a bounding waypoint was not reached. A missing measurement is
    -- not a zero-length one and is excluded from statistics rather than
    -- counted as instantaneous.
    duration_s       REAL,
    distance_m       REAL,
    avg_speed_kph    REAL,
    is_complete      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (trip_id, seq)
);

CREATE INDEX segments_stats ON segments (template_id, seq, local_date, duration_s);
CREATE INDEX segments_hour  ON segments (template_id, seq, local_hour);
CREATE INDEX segments_trip  ON segments (trip_id);

-- Deduplication is by filename: the recorder names files by their UTC start
-- time, so a name identifies a journey uniquely and a file is never ingested
-- twice. Files that fail to parse are recorded too, so that a broken file is
-- not retried on every refresh.
CREATE TABLE processed_files (
    filename      TEXT PRIMARY KEY,
    trip_id       TEXT REFERENCES trips(id) ON DELETE SET NULL,
    sha256        TEXT,
    file_time_utc TEXT,
    processed_at  TEXT NOT NULL,
    status        TEXT NOT NULL,
    error_message TEXT
);

CREATE INDEX processed_files_time ON processed_files (file_time_utc DESC);
