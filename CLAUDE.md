# Route Tracker — working notes

Context for continuing work on this repository. The README is the user-facing
description; this file is the reasoning behind the code and the state of the
deployment.

## What it is

Measures how long each stretch of a regularly driven route takes, from GPX
recordings made by an off-the-shelf phone GPS logger. The owner drives their kid
to kindergarten and back, Mon–Fri, and wants per-stretch statistics — above all
**"is this stretch busy at this hour?"**

One Go binary: it serves the interface, imports recordings from a Google Drive
folder, and stores everything in one SQLite file. Server-rendered Go templates
with htmx; Leaflet and Chart.js are vendored into the binary. No Node, no asset
build, no second service.

## Layout

```
cmd/tracker            entry point, config, graceful shutdown, storage check
internal/geo           segment-to-circle geometry, projection, simplification
internal/gpx           GPX parsing and noise filtering
internal/matcher       crossing detection and measurement — pure, no I/O
internal/stats         median/percentiles/MAD
internal/storage       SQLite schema, queries, embedded migrations
internal/service       importing, measuring, recomputing, statistics
internal/web           handlers, templates, static assets (all embedded)
```

68 tests across 8 packages. `internal/matcher` is where the real logic lives and
carries the most important ones.

## Decisions that must not be casually undone

Each of these looks like it could be simplified. Each was chosen for a reason
that is not obvious from the code alone.

**Crossing detection works on the chord between consecutive fixes, not on
whether a fix lies inside the waypoint radius.** A phone logger samples every
3–5 s; at 50 km/h that is 40–70 m between fixes, so a pass through a 20 m radius
usually contains *no fix at all*. Testing the fixes alone misses most crossings,
and widening the radius until it works destroys the timing precision. See
`geo.IntersectCircle` and `matcher.findPasses`.

**Waypoint assignment is a dynamic program, not a greedy scan.** The route is
out-and-back, so the track passes some waypoints twice. A greedy scan commits to
the first plausible pass and cannot revise it, and one wrong commitment corrupts
every later stretch. `matcher.assign`, ~60 lines, exact.

**Direction is derived, never configured.** The waypoint order is matched both
ways and the better fit wins; reverse trips are renumbered into canonical order
so a given `seq` always denotes the same physical road and both directions pool
into one bucket. There is deliberately no `trip_type` and no time-of-day rule —
the owner explicitly does not want a drop-off/pickup distinction.

**A journey ends at the first pass of the final waypoint** (`takeFirstArrival`).
A recorder left running after arrival otherwise inflates the last stretch by
however long it stayed on, chosen by an incidental tiebreak. Nothing is trimmed
when the final waypoint was *missed*, because then there is no arrival to trim at
and the track around the missed waypoint is exactly what the map needs to show
why.

**A recording reaching fewer than two waypoints produces no trip**
(`service.ErrNotOnRoute`), but the file is still recorded as seen so it is not
reconsidered every refresh. Because that would otherwise be irreversible, skipped
recordings are listed on the dashboard with a **Reconsider** button.

**One model of the Earth.** `geo.Distance` uses the same ellipsoidal local frame
as the crossing geometry. A spherical haversine agrees to within a metre at
latitude 52 by coincidence and differs by ~0.5% in the tropics, which made a
stretch's reported length disagree with the geometry that found its endpoints.
Do not reintroduce haversine.

**Statistics are computed in Go, not SQL.** SQLite has no percentile functions,
and the data is a few tens of thousands of rows at most. `stats.Summarise`.

**`local_date` / `local_weekday` / `local_hour` are denormalised at ingest** in
the configured `APP_TZ`. Hour-of-day is the primary analysis axis, so
reinterpreting UTC at query time would misbucket journeys and break across DST.

**The renderer converts instants to `APP_TZ`.** Storage returns UTC; the
template helpers must not format whatever location a `time.Time` carries. This
was a real bug — every time of day displayed in UTC while the dates beside them
were local.

**Raw GPX is kept forever and is the only source of truth.** Crossings, segments
and the drawn path are derived and always rebuilt wholesale, never patched.
`template_version` and `matcher.AlgoVersion` on each trip drive recomputation:
the stale query *is* the work queue, so there is no queue table and nothing to
recover after a crash. Bump `AlgoVersion` when changing the algorithm and every
stored trip recomputes on next start.

**The filename is the deduplication key.** Recordings are named
`yyyyMMddHHmmss.gpx` in **UTC**. Files that fail are recorded too, so a broken
one is not retried forever.

**Only one route may be active**, enforced by a partial unique index
(migration `0002`). The first waypoint creates the route on demand, and two
concurrent requests could each create one while only the oldest is ever shown —
making waypoints appear to vanish.

**Drive access is a service account**, with the token exchange delegated to
`golang.org/x/oauth2` and the two Drive calls written as plain HTTP. Do not pull
in `google.golang.org/api`: it adds ~100 modules for a list and a download. Note
a Drive **API key cannot work** — it identifies a project, not a principal, and
only reads public files. The folder is checked before listing so that the usual
mistake (never sharing it) names the address to share with instead of looking
like an empty folder.

**No authentication**, on the assumption of Tailscale-only access. Keep
`http.MaxBytesReader` and the `<!DOCTYPE` rejection regardless: unauthenticated
XML parsing is the one genuinely exposed surface.

## Conventions

- **Go code follows the [Google Go Style Guide](https://google.github.io/styleguide/go/).**
  Interfaces are declared by the consumer (`service.Source`), `context.Context`
  first, doc comments start with the symbol's name, errors lowercase and wrapped
  with `%w`. Prefer the standard library over another dependency.
- Comments explain *why*, not what. Several of the decisions above exist as
  comments at the relevant code; keep them in sync if the reasoning changes.
- Port **8381** (the owner's home lab uses 83xx for small apps).
- Timezone **America/El_Salvador** (UTC−6, no DST).
- Every change should keep `gofmt -l .`, `go vet ./...` and `go test ./...` clean.

## Running and testing

```sh
make test            # matcher tests are the ones that matter
make run             # serves on :8381 against ./data
```

No cloud folder is needed locally: drop `.gpx` files into `./data/inbox` and
press **Refresh**. `internal/service/service_test.go` has helpers that synthesise
realistic tracks (correct fix spacing for driving speed), which is the fastest
way to exercise the whole import path.

When changing behaviour, prefer adding to the table-driven matcher tests. They
cover the cases that actually break: a fast pass with no fix inside the radius, a
boundary blip, a waypoint passed twice, a paused recording, a track starting
inside a waypoint, a missed waypoint, a reversed direction, and a recorder left
running past arrival.

## Deployment

Coolify on a home-lab server, built from this repo's Dockerfile, reached over
Tailscale. Multi-stage build to Alpine, ~27 MB. The entrypoint starts as root
only long enough to hand `/data` to uid 10001, then drops privileges with
`su-exec`; the tracker itself never runs as root.

Persistent storage must be a **Directory Mount**, host
`/mnt/storage/apps/route-tracker` → container `/data`. Runtime variables:
`APP_TZ`, `DRIVE_FOLDER_ID`, `GOOGLE_CREDENTIALS_B64`.

**Credentials must never be marked as build variables.** Coolify passes build
variables to the image as `ARG`, which records them in the image's build history
and prints them in the deployment log. Nothing here is needed at build time.

## Deployment state as of 2026-09-10

- The application deploys and runs; the Drive credentials and folder are
  configured.
- **Persistent storage is not yet attached.** `/data` is inside the container, so
  the route and all imported trips are lost on every redeployment. The dashboard
  shows a red banner and the startup log warns while this is true
  (`cmd/tracker/storage_check.go`); both disappear once a mount is real. The fix
  is a Coolify Directory Mount as above — no code change is pending.
- Once storage is attached the route needs defining: four waypoints, and **not**
  one at the house. A waypoint there is crossed when Record was pressed, so that
  stretch measures finding one's phone rather than driving — and because the route
  is out-and-back it would be clean arriving and noisy departing, giving a
  bimodal median for no visible reason. First junction or end of the street.
- No trips have been successfully analysed and retained yet.
- Worth confirming on the next pass: that the service account key in use is the
  current one, and that no credential is marked as a build variable in Coolify.
