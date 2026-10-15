#!/bin/sh
# Generate and publish committed fixtures in the canonical linux/amd64 image.
set -eu

script_dir=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
repo_input=${VERIFY_ENV_REPO:-"$script_dir/../.."}
attest=0
run_dir=
project=
docker_bin=${DOCKER:-docker}

usage() {
    cat <<'EOF'
Usage: tools/verify-env/generate-fixtures.sh [--attest]

Generate a private candidate in the pinned linux/amd64 environment. Validate
the complete candidate before replacing tools/goldenrec/fixtures/out.
Without --attest, require exact source-manifest.sha256 bytes. With --attest,
replace source-manifest.sha256 atomically with the validated manifest.
EOF
}

case ${1-} in
    '')
        ;;
    --attest)
        attest=1
        ;;
    --help)
        usage
        exit 0
        ;;
    *)
        usage >&2
        exit 64
        ;;
esac
if [ "$#" -gt 1 ]; then
    usage >&2
    exit 64
fi

case $repo_input in
    /*)
        ;;
    *)
        repo_input=$PWD/$repo_input
        ;;
esac
if ! repo=$(CDPATH= cd "$repo_input" 2>/dev/null && pwd -P); then
    echo "generate-fixtures.sh: invalid Jellyport root: $repo_input" >&2
    exit 1
fi

fixtures=$repo/tools/goldenrec/fixtures
source_manifest=$fixtures/source-manifest.sha256
if [ ! -f "$source_manifest" ] || [ -L "$source_manifest" ]; then
    echo "generate-fixtures.sh: invalid source manifest: $source_manifest" >&2
    exit 1
fi

command -v "$docker_bin" >/dev/null 2>&1 || {
    echo "generate-fixtures.sh: Docker is required" >&2
    exit 1
}
"$docker_bin" compose version >/dev/null 2>&1 || {
    echo "generate-fixtures.sh: Docker Compose v2 is required" >&2
    exit 1
}

if ! run_dir=$(mktemp -d "$fixtures/.fixture-generation.XXXXXX"); then
    echo "generate-fixtures.sh: cannot create a private candidate directory" >&2
    exit 1
fi
run_name=${run_dir##*/}
project_suffix=$(printf '%s' "$run_name" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')
project=jellyport-fixture-$project_suffix

VERIFY_ENV_REPO=$repo
VERIFY_ENV_UID=$(id -u)
VERIFY_ENV_GID=$(id -g)
export VERIFY_ENV_REPO VERIFY_ENV_UID VERIFY_ENV_GID

compose() {
    "$docker_bin" compose --project-name "$project" -f "$script_dir/compose.yaml" "$@"
}
cleanup() {
    if [ -n "$project" ]; then
        compose down --rmi local >/dev/null 2>&1 || true
    fi
    if [ -n "$run_dir" ]; then
        rm -rf "$run_dir"
    fi
}
trap cleanup EXIT HUP INT TERM

compose build fixture-generator
compose run --rm --no-deps --entrypoint /bin/sh fixture-generator -c '
  for probe in tools/verify-env/fixture-generator-entrypoint.sh tools/goldenrec/fixtures/source-manifest.sha256; do
    if ! test -r "/work/$probe"; then
      echo "generate-fixtures.sh: fixture container cannot read the Jellyport checkout at /work; check the Docker bind-mount or Docker Desktop file sharing" >&2
      exit 1
    fi
  done
'

set -- \
    --candidate-name "$run_name"
if [ "$attest" -eq 1 ]; then
    set -- "$@" --attest
fi
compose run --rm --no-deps fixture-generator "$@"
