#!/bin/sh
# tools/coldstart/run.sh — Docker lifecycle for the cold-start gate.
#
# Starts one published Deadfin image against a genuinely empty data directory
# and the canonical fixture tree. A keeper container stays mounted on the
# tmpfs media volume through staging and cleanup so the volume keeps its
# bytes. The verified fixture bytes stream into the volume through the
# candidate image's /usr/bin/tar over stdin, the staged bytes are compared
# against source-manifest.sha256, then the server starts with the media
# volume read-only at /media and a fresh tmpfs at /var/lib/deadfin.
#
# No chown, chmod, --user override, container restart, or failed-step retry.
# The tmpfs ownership mirrors the deployment fsGroup contract through mount
# options, not a repair command.
#
# Usage:
#   run.sh                  — run the full cold-start gate
#   run.sh cleanup          — remove all resources with the cold-start label
set -eu

repo_root=$(CDPATH= cd "$(dirname "$0")/../.." && pwd -P)
fixtures=$repo_root/tools/goldenrec/fixtures
source_manifest=$fixtures/source-manifest.sha256
out=$fixtures/out

docker_bin=${DOCKER:-docker}

label_key=deadfin.cold-start.run

fail() {
    echo "cold-start: $1" >&2
    exit 1
}

# Remove all resources that carry the cold-start label. Query and removal
# errors fail the job instead of being suppressed. A remaining labeled
# resource is a job failure.
cleanup_all() {
    status=0
    set +e

    # Containers: remove every container with any cold-start run label.
    containers=$("$docker_bin" ps -a --filter "label=$label_key" --format '{{.Names}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled containers" >&2
        status=1
    fi
    if [ -n "$containers" ]; then
        for name in $containers; do
            "$docker_bin" rm -f "$name" >&2
            if [ "$?" -ne 0 ]; then
                echo "cold-start: cleanup failed to remove container $name" >&2
                status=1
            fi
        done
    fi

    # Networks: remove every network with any cold-start run label.
    networks=$("$docker_bin" network ls --filter "label=$label_key" --format '{{.Name}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled networks" >&2
        status=1
    fi
    if [ -n "$networks" ]; then
        for name in $networks; do
            "$docker_bin" network rm "$name" >&2
            if [ "$?" -ne 0 ]; then
                echo "cold-start: cleanup failed to remove network $name" >&2
                status=1
            fi
        done
    fi

    # Volumes: remove every volume with any cold-start run label.
    volumes=$("$docker_bin" volume ls --filter "label=$label_key" --format '{{.Name}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled volumes" >&2
        status=1
    fi
    if [ -n "$volumes" ]; then
        for name in $volumes; do
            "$docker_bin" volume rm "$name" >&2
            if [ "$?" -ne 0 ]; then
                echo "cold-start: cleanup failed to remove volume $name" >&2
                status=1
            fi
        done
    fi

    # Fail if any labeled resource remains.
    remaining=$("$docker_bin" ps -a --filter "label=$label_key" --format '{{.Names}}')
    if [ "$?" -ne 0 ]; then
        echo "cold-start: cleanup failed to re-query labeled containers" >&2
        status=1
    elif [ -n "$remaining" ]; then
        echo "cold-start: labeled containers remain after cleanup: $remaining" >&2
        status=1
    fi
    remaining_volumes=$("$docker_bin" volume ls --filter "label=$label_key" --format '{{.Name}}')
    if [ "$?" -ne 0 ]; then
        echo "cold-start: cleanup failed to re-query labeled volumes" >&2
        status=1
    elif [ -n "$remaining_volumes" ]; then
        echo "cold-start: labeled volumes remain after cleanup: $remaining_volumes" >&2
        status=1
    fi
    remaining_networks=$("$docker_bin" network ls --filter "label=$label_key" --format '{{.Name}}')
    if [ "$?" -ne 0 ]; then
        echo "cold-start: cleanup failed to re-query labeled networks" >&2
        status=1
    elif [ -n "$remaining_networks" ]; then
        echo "cold-start: labeled networks remain after cleanup: $remaining_networks" >&2
        status=1
    fi

    set -e
    exit "$status"
}

# Cleanup-only mode: invoked by the workflow cleanup step with if: always().
if [ "${1:-}" = "cleanup" ]; then
    cleanup_all
fi

# --- Full cold-start gate run ---

: "${IMAGE_REFERENCE:?IMAGE_REFERENCE must name the pinned image reference, for example ghcr.io/deadfin-org/deadfin@sha256:...}"
: "${IMAGE_DIGEST:?IMAGE_DIGEST must be the requested sha256:<hex> digest}"

# Optional; empty when the dispatch did not supply one.
COMMIT_SHA=${COMMIT_SHA:-}

# The control mode: positive runs the canonical tree. negative removes one
# library source after configuration so the scan records a Failed task
# result while the item count stays nonzero. Default positive preserves
# the current dispatch behavior.
CONTROL=${CONTROL:-positive}
if [ "$CONTROL" != "positive" ] && [ "$CONTROL" != "negative" ]; then
    fail "CONTROL must be positive or negative, got: $CONTROL"
fi
echo "cold-start: control=$CONTROL"

run_id=$(date +%Y%m%d-%H%M%S)-$$
run_label=$run_id
container_name=deadfin-coldstart-$run_id
keeper_name=deadfin-coldstart-keeper-$run_id
network_name=deadfin-coldstart-net-$run_id
media_volume=deadfin-coldstart-media-$run_id

uid=65532
gid=65532
tmpfs_mode=0700

# Deadfin listens on port 8096 (image ExposedPorts 8096/tcp). API probes reach
# the server on that port at its container IP address. No host port is
# published.
server_port=8096

initial_container_id=
final_container_id=
restart_count=
verify_dir=

require_tools() {
    for tool in "$docker_bin" python3 sha256sum find tar; do
        command -v "$tool" >/dev/null 2>&1 || { echo "cold-start: $tool is required on the host" >&2; exit 127; }
    done
}

require_fixture_tree() {
    if [ ! -f "$source_manifest" ] || [ -L "$source_manifest" ]; then
        echo "cold-start: source-manifest.sha256 is missing at $source_manifest" >&2
        echo "cold-start: run tools/verify-env/generate-fixtures.sh before this script" >&2
        exit 1
    fi
    if [ ! -d "$out/movies" ] || [ ! -d "$out/tv" ] || [ ! -d "$out/music" ] || [ ! -d "$out/photos" ]; then
        echo "cold-start: the canonical fixture tree is incomplete at $out" >&2
        echo "cold-start: run tools/verify-env/generate-fixtures.sh before this script" >&2
        exit 1
    fi
}

# Remove only resources that carry this run label. A failed cleanup or a
# remaining labeled resource is a job failure. Query and removal errors fail
# the job instead of being suppressed.
cleanup_run() {
    status=$?
    set +e
    "$docker_bin" rm -f "$container_name" "$keeper_name" >&2
    rm_status=$?
    "$docker_bin" network rm "$network_name" >&2
    net_status=$?
    "$docker_bin" volume rm "$media_volume" >&2
    vol_status=$?
    if [ -n "$verify_dir" ]; then
        rm -rf "$verify_dir"
    fi
    if [ "$rm_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to remove a container ($container_name or $keeper_name)" >&2
        status=1
    fi
    if [ "$net_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to remove network $network_name" >&2
        status=1
    fi
    if [ "$vol_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to remove volume $media_volume" >&2
        status=1
    fi
    # Fail if any resource still carries this run label.
    remaining=$("$docker_bin" ps -a --filter "label=$label_key=$run_label" --format '{{.Names}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled containers" >&2
        status=1
    elif [ -n "$remaining" ]; then
        echo "cold-start: labeled containers remain after cleanup: $remaining" >&2
        status=1
    fi
    remaining_volumes=$("$docker_bin" volume ls --filter "label=$label_key=$run_label" --format '{{.Name}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled volumes" >&2
        status=1
    elif [ -n "$remaining_volumes" ]; then
        echo "cold-start: labeled volumes remain after cleanup: $remaining_volumes" >&2
        status=1
    fi
    remaining_networks=$("$docker_bin" network ls --filter "label=$label_key=$run_label" --format '{{.Name}}')
    query_status=$?
    if [ "$query_status" -ne 0 ]; then
        echo "cold-start: cleanup failed to query labeled networks" >&2
        status=1
    elif [ -n "$remaining_networks" ]; then
        echo "cold-start: labeled networks remain after cleanup: $remaining_networks" >&2
        status=1
    fi
    set -e
    exit "$status"
}
trap cleanup_run EXIT HUP INT TERM

require_tools
require_fixture_tree

if [ -z "$run_id" ]; then
    fail "run id is empty"
fi

echo "cold-start: run_id=$run_id"
echo "cold-start: image_reference=$IMAGE_REFERENCE"
echo "cold-start: image_digest=$IMAGE_DIGEST"
if [ -n "$COMMIT_SHA" ]; then
    echo "cold-start: commit_sha=$COMMIT_SHA"
fi
# Network: one run-unique internal network. The server attaches only to that
# network. No host port is published. API probes reach the server on its
# container IP address inside the internal network.
"$docker_bin" network create --driver bridge --internal \
    --label "$label_key=$run_label" \
    "$network_name" >/dev/null \
    || fail "could not create network $network_name"
internal=$("$docker_bin" network inspect --format '{{.Internal}}' "$network_name" \
    || fail "could not inspect network $network_name")
echo "cold-start: network $network_name Internal=$internal"
if [ "$internal" != "true" ]; then
    fail "network $network_name Internal is $internal, expected true"
fi

# Media volume: a tmpfs-backed local-driver volume. The mount options set
# owner 65532:65532 and mode 0700. No chmod or chown command is used. The
# ownership mirrors the deployment fsGroup contract through mount options.
"$docker_bin" volume create \
    --driver local \
    --opt type=tmpfs \
    --opt device=tmpfs \
    --opt "o=uid=$uid,gid=$gid,mode=0700" \
    --label "$label_key=$run_label" \
    "$media_volume" \
    || fail "could not create media volume $media_volume"

# Keeper container: a local-driver tmpfs volume loses its bytes when its last
# mounted container exits. Start a keeper container before staging and keep it
# mounted on the media volume through staging, verification, server start, and
# cleanup. Fail if the keeper exits before cleanup.
"$docker_bin" run -d \
    --name "$keeper_name" \
    --label "$label_key=$run_label" \
    --label deadfin.cold-start.role=keeper \
    --network "$network_name" \
    --restart=no \
    -v "$media_volume:/media" \
    --entrypoint /bin/sleep \
    "$IMAGE_REFERENCE" 3600 \
    || fail "could not start the keeper container"
keeper_state=$("$docker_bin" inspect --format '{{.State.Running}}' "$keeper_name" \
    || fail "could not inspect the keeper container")
if [ "$keeper_state" != "true" ]; then
    fail "keeper container is not running (state: $keeper_state)"
fi
echo "cold-start: keeper container started: $keeper_name"

# Inspect /media inside the keeper. Require owner 65532:65532 and mode 0700.
# The mount options on the tmpfs volume set the ownership and mode without a
# chmod or chown command.
media_stat=$("$docker_bin" exec "$keeper_name" /usr/bin/stat -c '%u:%g %A' /media 2>/dev/null || true)
echo "cold-start: media mount stat: $media_stat"
if [ -z "$media_stat" ]; then
    fail "could not stat /media inside the keeper container"
fi
media_owner=$(printf '%s' "$media_stat" | cut -d' ' -f1)
media_mode=$(printf '%s' "$media_stat" | cut -d' ' -f2)
if [ "$media_owner" != "${uid}:${gid}" ]; then
    fail "media mount owner is $media_owner, expected ${uid}:${gid}"
fi
if printf '%s' "$media_mode" | grep -qv 'drwx------'; then
    fail "media mount mode is $media_mode, expected drwx------ (0700)"
fi

# State volume: a fresh tmpfs for /var/lib/deadfin, owner 65532:65532, mode
# 0700. This is mounted directly on the server container through --mount
# type=tmpfs, not as a named volume, so it is always fresh and does not need
# a separate volume create. No bind-mounted host directory, no chown, no chmod.

# Stage the verified fixture bytes into the media volume through the candidate
# image's /usr/bin/tar over stdin. The host streams the tar archive to the
# keeper container's tar through docker exec. No docker cp, no bind-mounted
# tar file, no chmod, no --user: the keeper container runs with the published
# default user 65532:65532. The tmpfs volume is owned 65532:65532 with mode
# 0700, so the default user can create files.
echo "cold-start: staging fixture bytes into $media_volume"
tar -cf - -C "$out" . | "$docker_bin" exec -i "$keeper_name" /usr/bin/tar -C /media --no-same-permissions -xf - \
    || fail "could not stage fixture bytes into the media volume"

# Compare the staged bytes with source-manifest.sha256 before the server
# starts. The candidate runtime image is minimal and may not include sha256sum
# or find. Stream the volume back through the keeper container's tar to a host
# temp directory, then verify on the host where the full toolchain is
# available. The keeper container keeps the tmpfs alive through verification.
verify_dir=$(mktemp -d "${TMPDIR:-/tmp}/coldstart-verify.XXXXXX")
"$docker_bin" exec "$keeper_name" /usr/bin/tar -C /media -cf - . \
    | tar -xf - -C "$verify_dir" \
    || fail "could not stream staged bytes back for verification"

manifest_count=$(grep -c . "$source_manifest" || true)
actual_count=$(find "$verify_dir" -type f ! -name MANIFEST.sha256 ! -name .goldenrec-fixtures-owner | wc -l)
echo "cold-start: staged file count=$actual_count manifest entry count=$manifest_count"
if [ "$actual_count" -ne "$manifest_count" ]; then
    fail "staged file count ($actual_count) differs from the manifest ($manifest_count)"
fi

(cd "$verify_dir" && sha256sum -c "$source_manifest") \
    || fail "staged bytes do not match source-manifest.sha256"
echo "cold-start: staged bytes match source-manifest.sha256"
rm -rf "$verify_dir"
verify_dir=

# Start the server. One container, default user and entrypoint, --restart=no.
# The server attaches only to the internal network. No host port is
# published. Media volume read-only at /media. State volume at
# /var/lib/deadfin. No --user override.
initial_container_id=$("$docker_bin" run -d \
    --name "$container_name" \
    --label "$label_key=$run_label" \
    --label deadfin.cold-start.role=server \
    --network "$network_name" \
    --restart=no \
    -v "$media_volume:/media:ro" \
    --tmpfs "/var/lib/deadfin:uid=$uid,gid=$gid,mode=$tmpfs_mode" \
    "$IMAGE_REFERENCE") \
    || fail "could not start the server container"

# Inspect every attached network. Require exactly one attached network with
# Internal=true.
net_count=$("$docker_bin" inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$container_name" | wc -w)
if [ "$net_count" -ne 1 ]; then
    fail "server container is attached to $net_count networks, expected exactly 1"
fi
attached_net=$("$docker_bin" inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}' "$container_name")
attached_internal=$("$docker_bin" network inspect --format '{{.Internal}}' "$attached_net" \
    || fail "could not inspect the attached network $attached_net")
if [ "$attached_internal" != "true" ]; then
    fail "attached network $attached_net Internal is $attached_internal, expected true"
fi
echo "cold-start: server attached to $attached_net (Internal=$attached_internal)"

# Resolve the server container IP address on the internal network. API
# probes reach the server on that IP address at the Deadfin listen port.
# No host port is published.
server_ip=$("$docker_bin" inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container_name")
[ -n "$server_ip" ] || fail "could not resolve the server container IP address"
echo "cold-start: server_ip=$server_ip server_port=$server_port"

# Trim to the full 64-char container ID (docker run -d may print a short id).
initial_container_id=$("$docker_bin" inspect --format '{{.Id}}' "$container_name" || printf '%s' "$initial_container_id")
echo "cold-start: initial_container_id=$initial_container_id"

# Run the assertions. The verify client connects to the server container IP.
"$docker_bin" exec "$container_name" /bin/sh -c 'echo cold-start: container alive before assertions' \
    || fail "server container exited before the assertions began"

# Run the configuration phase. The verify client connects to the server
# container IP. In positive mode this is the only phase call site; the
# scan phase runs in the same process. In negative mode the configuration
# phase runs here, the scan phase runs after the source removal.
verify_env="PYTHONUNBUFFERED=1 IMAGE_DIGEST=$IMAGE_DIGEST COMMIT_SHA=$COMMIT_SHA \
RUN_LABEL=$run_label CONTAINER_NAME=$container_name"
verify_url="http://$server_ip:$server_port"
verify_policy="$repo_root/tools/goldenrec/provider-policy.json"

if [ "$CONTROL" = "positive" ]; then
    # Positive control: run the configuration and scan phases in one process.
    env $verify_env python3 "$repo_root/tools/coldstart/verify.py" \
        "$verify_url" "$container_name" "$run_label" "$verify_policy" \
        || fail "cold-start assertions failed"
else
    # Negative control: run the configuration phase, remove one library
    # source through the keeper container, then run the scan phase. The
    # scan phase must fail on the task-result assertion, not on the item
    # count. The three remaining sources keep the item count nonzero.
    env $verify_env python3 "$repo_root/tools/coldstart/verify.py" \
        "$verify_url" "$container_name" "$run_label" "$verify_policy" configure \
        || fail "cold-start configuration phase failed"

    # Remove /media/photos through the keeper container. The keeper is
    # mounted on the media volume, which the server mounts read-only. The
    # server cannot remove the source itself.
    echo "cold-start: removing /media/photos for negative control"
    "$docker_bin" exec "$keeper_name" /bin/sh -c 'rm -rf /media/photos' \
        || fail "could not remove /media/photos through the keeper container"

    # Make sure that /media/photos is absent.
    if "$docker_bin" exec "$keeper_name" /bin/sh -c 'test -d /media/photos'; then
        fail "/media/photos still exists after removal"
    fi
    # Make sure that /media/movies, /media/tv, and /media/music remain present.
    for source in movies tv music; do
        if ! "$docker_bin" exec "$keeper_name" /bin/sh -c "test -d /media/$source"; then
            fail "/media/$source is absent after the negative-control removal"
        fi
    done
    echo "cold-start: /media/photos removed, /media/movies /media/tv /media/music present"

    # Run the scan phase after the removal. Do not add an expected-failure
    # wrapper. The scan phase exits nonzero on the task-result assertion.
    env $verify_env python3 "$repo_root/tools/coldstart/verify.py" \
        "$verify_url" "$container_name" "$run_label" "$verify_policy" scan \
        || fail "cold-start assertions failed"
fi
# Evidence: require the final container ID to equal the initial container ID.
final_container_id=$("$docker_bin" inspect --format '{{.Id}}' "$container_name" || true)
if [ -z "$final_container_id" ]; then
    fail "could not read the final container ID"
fi
if [ "$final_container_id" != "$initial_container_id" ]; then
    fail "final container ID ($final_container_id) differs from the initial ID ($initial_container_id)"
fi
echo "cold-start: final_container_id=$final_container_id"

# Evidence: require Docker RestartCount to equal zero.
restart_count=$("$docker_bin" inspect --format '{{.RestartCount}}' "$container_name" || true)
if [ "$restart_count" != "0" ]; then
    fail "RestartCount is $restart_count, expected 0"
fi
echo "cold-start: restart_count=$restart_count"

# Evidence: require the keeper container to still be running. A local-driver
# tmpfs volume loses its bytes when its last mounted container exits, so the
# keeper must survive through the assertions to keep the media volume alive.
keeper_state=$("$docker_bin" inspect --format '{{.State.Running}}' "$keeper_name" || true)
if [ "$keeper_state" != "true" ]; then
    fail "keeper container $keeper_name is not running (state: $keeper_state)"
fi
echo "cold-start: keeper=$keeper_name still running"

echo "cold-start: gate passed"
