# Route Tracker

Measures how long each stretch of a regularly driven route takes, from GPX
recordings made by an off-the-shelf phone logger.

The daily drive is divided into stretches by waypoints you place yourself. Every
recording is matched against those waypoints, the time between consecutive
crossings becomes a measurement, and the statistics page answers the question
the whole thing exists for: **is this stretch busy at this hour?**

## What it is

One Go binary. It serves the interface, imports recordings, and stores
everything in a single SQLite file. Pages are rendered on the server and updated
in place with htmx; Leaflet draws the maps and Chart.js the graphs. All four
front-end libraries are vendored into the binary, so the tracker needs no
outbound network access and there is no JavaScript build step.

```
cmd/tracker          entry point, configuration, graceful shutdown
internal/geo         the segment-to-circle geometry
internal/gpx         GPX parsing and noise filtering
internal/matcher     crossing detection and stretch measurement (pure, no I/O)
internal/stats       medians, percentiles, outlier detection
internal/storage     SQLite schema and queries
internal/service     importing, measuring, recomputing
internal/web         handlers, templates and static assets
```

## How the measuring works

A phone logger samples every three to five seconds. At 50 km/h that puts its
fixes 40 to 70 metres apart, so a pass through a 20 metre waypoint often
contains **no recorded fix at all**. Asking "did a fix land inside the circle?"
would therefore miss most crossings, and widening the radius until it didn't
would make the crossing times meaningless.

So the unit of detection is the straight line between consecutive fixes. For
each waypoint the tracker solves where that line enters the circle and
interpolates the time, which recovers crossings no fix witnessed directly. Two
consequences worth knowing:

- **A pass is a run, not a point.** Entry and exit use different radii, so a
  track grazing the boundary reads as one pass rather than several.
- **Nothing is interpolated across a break.** A new GPX track segment, a gap
  over a minute, or an implied speed above 150 km/h means the straight line is
  fiction, and the waypoint is left unmatched instead.

Waypoints are then assigned in order by a small dynamic program rather than a
greedy scan. This matters because an out-and-back route passes some waypoints
twice: a greedy scan commits to the first plausible pass and cannot revise it,
and one wrong commitment corrupts every later stretch.

**Direction is derived, not configured.** The waypoint order is tried both ways
and the better fit wins, so the drive out and the drive back are both
recognised without being told which is which. Reverse trips are renumbered into
canonical order, so a given stretch number always means the same piece of road
and both directions pool into one bucket. They can still be separated with the
direction filter, which is worth doing if a road is plausibly jammed one way and
clear the other at the same hour.

**Nothing is discarded.** If a waypoint is never reached, the trip is recorded
as partial and the stretches touching that waypoint carry no duration — not a
zero, and never folded into a neighbouring stretch. Unusually slow journeys are
flagged in the charts but kept: a drive that took twice as long really did.

## Importing recordings

Record with any app that exports GPX (Open GPX Tracker and Geo Tracker both
work), and have it write into a cloud folder. Name each file for the UTC instant
the drive began, `yyyyMMddHHmmss.gpx`, for example `20260909064908.gpx`. **The
filename is the deduplication key**, so a file is imported exactly once however
often the folder is synced.

Press **Refresh** on the dashboard. That runs `rclone copy` from the configured
remote into `$DATA_DIR/inbox`, imports anything not seen before, and recomputes
whatever the route change queue is holding. It is deliberately manual: the
recordings arrive twice a day, and a button gives a clear moment to see what
happened without a scheduler to reason about.

Files that cannot be read are recorded as failures so they are not retried on
every refresh.

With no remote configured, the tracker still works — anything dropped into
`$DATA_DIR/inbox` is picked up on the next refresh. That is the easiest way to
try it out and to import a backlog.

## Editing the route

The route page has a map and a waypoint table. Click the map to fill in
coordinates for a new waypoint, and pick a recent trip as a backdrop to place it
on a road you actually drove rather than one guessed from the tiles.

Two things worth getting right:

- **Do not put the first waypoint at your house.** Its crossing time is when you
  pressed record, so the first stretch would measure you finding your phone. Put
  it at the end of your street or the first junction.
- **Radius around 20 to 25 metres.** Wide enough for ordinary GPS error near
  buildings, narrow enough that two waypoints on the same street stay distinct.
  Mark a waypoint *optional* where two roads run too close to tell apart; a
  missing optional waypoint does not make a trip partial.

Any waypoint edit raises the route version, and every stored trip is recomputed
against the new geometry. The retained source files are the only source of
truth, so this is always safe and takes a couple of seconds. Trips are never
pinned to an old route version — a chart mixing stretches measured against
different geometry would be worse than useless.

## Configuration

Everything is read from the environment.

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port (`ADDR` overrides with a full address) |
| `DATA_DIR` | `./data` | Database, retained recordings and the inbox |
| `DB_PATH` | `$DATA_DIR/tracker.db` | SQLite file |
| `APP_TZ` | `UTC` | IANA zone that dates and hours are reported in |
| `ANCHOR` | `entry` | `entry` or `closest` — see below |
| `DRIVE_REMOTE` | — | rclone remote name, e.g. `gdrive` |
| `DRIVE_FOLDER` | — | Path within the remote to mirror |
| `RCLONE_CONFIG_B64` | — | Base64 of an `rclone.conf`, written at startup |
| `MAX_UPLOAD_BYTES` | `26214400` | Largest accepted recording |

`APP_TZ` matters more than it looks: hour of day is the main axis of the whole
report, so local dates and hours are computed at import in this zone rather than
by reinterpreting UTC at query time, which would misbucket drives near midnight
and break across daylight saving changes.

`ANCHOR` chooses what bounds a stretch. `entry` is first entry into the radius,
which is the natural reading, but it shifts if you later widen a radius, so old
durations change for reasons that have nothing to do with driving. `closest`
uses the moment of closest approach, which is unaffected by radius edits. Both
instants are always stored, so switching is just a recompute.

## Deploying with Coolify

Point Coolify at this repository and let it build the Dockerfile — there is
nothing else to build.

1. Add a **persistent volume** mounted at `/data`. It holds the database, the
   inbox and the retained recordings. Backing it up is copying it.
2. Set `APP_TZ` to your zone.
3. To pull from cloud storage, configure the remote once on any machine with
   `rclone config`, then set `RCLONE_CONFIG_B64` to `base64 -w0 ~/.config/rclone/rclone.conf`,
   along with `DRIVE_REMOTE` and `DRIVE_FOLDER`. The value is a credential, so
   put it in Coolify's environment panel rather than in the repository.
4. `/healthz` is the health endpoint.

There is no authentication, on the assumption that the tracker is reachable only
over a private network. Check that Coolify is not also publishing it on a public
hostname. The upload path caps request size and refuses documents carrying a DTD
regardless, since parsing XML from an unauthenticated caller is the one place
where "it's only my home lab" would bite.

## Development

```sh
make test          # the matcher tests are the ones that matter
make run           # serves on :8080 against ./data
```

Drop `.gpx` files into `./data/inbox` and press Refresh; no remote needed.

The matcher is pure — it takes a route and a track and returns measurements,
with no database or HTTP involved — so its behaviour is pinned by table-driven
tests covering the cases that actually break: a fast pass with no fix inside the
radius, a boundary blip, a waypoint passed twice, a paused recording, a track
starting inside a waypoint, an unreachable waypoint, and a reversed direction.
Changing the algorithm means incrementing `matcher.AlgoVersion`, which recomputes
every stored trip on the next start.
