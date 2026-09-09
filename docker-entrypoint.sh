#!/bin/sh
# Prepares the data directory and then runs the tracker as an unprivileged user.
#
# In deployment the data directory is a bind mount from the host, and a bind
# mount keeps the host's ownership rather than taking the image's. The host
# directory is therefore usually owned by root when it is first created, and the
# tracker could not write to it. Rather than requiring the operator to chown it
# by hand on the server, the container starts as root, hands the directory to
# its own user, and immediately drops privileges: the tracker itself never runs
# as root.
set -e

DATA_DIR="${DATA_DIR:-/data}"
RUN_AS_UID=10001
RUN_AS_GID=10001

if [ "$(id -u)" = "0" ]; then
	mkdir -p "$DATA_DIR"

	# Recurse only when the ownership is actually wrong, which is the
	# first-run case on an empty directory. Doing it unconditionally would
	# walk every retained recording on every restart.
	if [ "$(stat -c %u "$DATA_DIR")" != "$RUN_AS_UID" ]; then
		echo "entrypoint: taking ownership of $DATA_DIR for uid $RUN_AS_UID"
		# Not fatal. It fails under a container runtime whose root is itself
		# unprivileged, and the directory may already be writable by other
		# means, so the tracker's own startup check is left to decide: it
		# reports the problem in terms of the command that fixes it.
		chown -R "$RUN_AS_UID:$RUN_AS_GID" "$DATA_DIR" ||
			echo "entrypoint: could not change ownership of $DATA_DIR; continuing"
	fi

	exec su-exec "$RUN_AS_UID:$RUN_AS_GID" "$@"
fi

# Already running as someone else, because the container was started with an
# explicit user. Nothing can be adjusted from here, so run as we are and let the
# tracker's own startup check report the problem if there is one.
exec "$@"
