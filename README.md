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

**The journey ends on arrival.** A recorder left running after you park keeps
producing fixes, and driving on afterwards often carries the track back through
the final waypoint. The earliest pass of that waypoint is taken as the arrival,
and everything after it is excluded from the duration, the distance and the
drawn path, so forgetting to stop recording does not read as a slower drive. The
arrival still has to follow the waypoint before it, so a route that runs close to
its own destination on the way there is not mistaken for having arrived early.
Nothing is trimmed when the final waypoint was missed altogether — there is no
arrival to trim at, and the track around the missed waypoint is exactly what the
map needs to show in order to work out why.

**A recording that does not follow the route is ignored.** Reaching fewer than
two waypoints means no stretch can be measured, which is what a test recording
or an unrelated drive looks like, so no trip is created. The file is still
recorded as seen and is not considered again on the next refresh. Ignored
recordings are listed on the dashboard with a **Reconsider** button, because one
skipped while the waypoints were still being placed deserves a second look once
they are right.

**Nothing else is discarded.** If a waypoint in the middle is never reached, the
trip is recorded as partial and the stretches touching that waypoint carry no
duration — not a zero, and never folded into a neighbouring stretch. Unusually
slow journeys are flagged in the charts but kept: a drive that took twice as long
really did.

## Importing recordings

Record with any app that exports GPX (Open GPX Tracker and Geo Tracker both
work), and have it write into a cloud folder. Name each file for the UTC instant
the drive began, `yyyyMMddHHmmss.gpx`, for example `20260909064908.gpx`. **The
filename is the deduplication key**, so a file is imported exactly once however
often the folder is synced.

Press **Refresh** on the dashboard. That lists the Drive folder, downloads
anything not already in `$DATA_DIR/inbox`, imports whatever has not been seen
before, and recomputes anything left stale by a route change. It is deliberately
manual: the recordings arrive twice a day, and a button gives a clear moment to
see what happened without a scheduler to reason about.

Files that cannot be read are recorded as failures so they are not retried on
every refresh.

With no folder configured, the tracker still works — anything dropped into
`$DATA_DIR/inbox` is picked up on the next refresh. That is the easiest way to
try it out and to import a backlog.

### Connecting the Drive folder

Access is by **service account**, so there is no consent screen to revisit and no
refresh token to store. Note that a Drive **API key will not work**: a key
identifies a project rather than a principal, and can only read public files.

1. In a Google Cloud project, enable the **Google Drive API**.
2. Create a **service account** and download a **JSON key** for it.
3. Open the Drive folder holding the recordings and **share it with the service
   account's address** — the one ending `…iam.gserviceaccount.com` — exactly as
   you would share with a person. **Viewer** is enough, and is all the tracker
   asks for: it requests the `drive.readonly` scope and only ever lists that one
   folder's direct children.
4. Set `DRIVE_FOLDER_ID` to the last path element of the folder's address, as in
   `https://drive.google.com/drive/folders/<this part>`, and
   `GOOGLE_CREDENTIALS_B64` to `base64 -w0 key.json`.

Step 3 is the one that gets forgotten. If it is missed the folder simply is not
visible to the account, which would otherwise look indistinguishable from an
empty folder, so the tracker checks the folder before listing it and says which
address to share with:

```
drive folder 1AbC… is not readable by tracker@project.iam.gserviceaccount.com;
share the folder with that address, giving it at least Viewer access
```

Downloads are written under a temporary name and renamed into place, so an
interrupted transfer cannot leave a truncated recording that would then be
treated as seen. Files already in the inbox are never re-downloaded or
overwritten.

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
| `PORT` | `8381` | Listen port (`ADDR` overrides with a full address) |
| `DATA_DIR` | `./data` | Database, retained recordings and the inbox |
| `DB_PATH` | `$DATA_DIR/tracker.db` | SQLite file |
| `APP_TZ` | `UTC` | IANA zone that dates and hours are reported in |
| `ANCHOR` | `entry` | `entry` or `closest` — see below |
| `DRIVE_FOLDER_ID` | — | Google Drive folder to read, from its address |
| `GOOGLE_CREDENTIALS_B64` | — | Base64 of a service account key |
| `GOOGLE_CREDENTIALS_FILE` | — | Path to a service account key, instead of the above |
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

**Storage.** In Coolify add a **persistent storage** entry of type *bind mount*:

| | |
|---|---|
| Host path | `/mnt/storage/apps/route-tracker` |
| Container path | `/data` |

That directory ends up holding `tracker.db`, `inbox/` (recordings pulled from
the cloud folder) and `gpx/` (the retained originals). Backing it up is copying
it; a SQLite database copied while the tracker is idle is a complete backup.

Nothing needs preparing on the server by hand. A bind mount keeps the *host's*
ownership rather than taking the image's, so a freshly created host directory
belongs to root and the tracker's unprivileged user could not write to it; the
container therefore starts as root only long enough to hand the directory over,
then drops to that user. The tracker process itself never runs as root.

**Check the storage really is attached.** Without it the tracker works perfectly
and silently loses everything on each deployment, which looks indistinguishable
from a tracker nobody has used yet. It therefore says so itself, in the startup
log and across the top of the dashboard:

```
THE DATA DIRECTORY IS NOT A MOUNTED VOLUME: everything stored will be lost
when this container is replaced, which a redeployment does
```

The dashboard also shows when the database was created. If that timestamp moves
with every deployment, the data directory is not being persisted, whatever the
storage settings claim. The quickest confirmation from the server is that the
host path is not empty:

```sh
ls -la /mnt/storage/apps/route-tracker     # expect tracker.db, gpx/, inbox/
docker inspect --format '{{json .Mounts}}' <container> | python3 -m json.tool
```

If the directory somehow cannot be handed over, the container says so and stops
rather than starting in a broken state, naming the directory and the command
that fixes it:

```
data directory /data is not writable by uid 10001;
if it is a bind mount, run: chown -R 10001:10001 /data
```

**Environment.** Set these in Coolify's environment panel, not in the repository:

- `APP_TZ` — your zone: `America/El_Salvador`.
- `DRIVE_FOLDER_ID` and `GOOGLE_CREDENTIALS_B64` to read the Drive folder; see
  below. The key is a credential, so it belongs in the environment panel rather
  than in the repository.

**Never mark the credential as a build variable.** A build variable is passed to
the image build as an `ARG`, which records it in the image's build history, where
anyone who can read the image can recover it — and prints it in the deployment
log. Docker warns about this itself:

```
SecretsUsedInArgOrEnv: Do not use ARG or ENV instructions for sensitive data
```

Nothing in the build needs any of these values; they are read at startup. Keep
them as runtime variables only. `GOOGLE_CREDENTIALS_FILE` avoids the question
altogether by pointing at a key file placed in the mounted data directory, which
never passes through the environment.

`/healthz` is the health endpoint.

There is no authentication, on the assumption that the tracker is reachable only
over a private network. Check that Coolify is not also publishing it on a public
hostname. The ingestion path caps file size and refuses documents carrying a DTD
regardless, since parsing XML from an unauthenticated caller is the one place
where "it's only my home lab" would bite.

## Development

```sh
make test          # the matcher tests are the ones that matter
make run           # serves on :8381 against ./data
```

Drop `.gpx` files into `./data/inbox` and press Refresh; no remote needed.

The matcher is pure — it takes a route and a track and returns measurements,
with no database or HTTP involved — so its behaviour is pinned by table-driven
tests covering the cases that actually break: a fast pass with no fix inside the
radius, a boundary blip, a waypoint passed twice, a paused recording, a track
starting inside a waypoint, an unreachable waypoint, and a reversed direction.
Changing the algorithm means incrementing `matcher.AlgoVersion`, which recomputes
every stored trip on the next start.
