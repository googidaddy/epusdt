#!/bin/sh
# Build only; never switch containers or pull Git automatically.
set -eu
: "${EPUSDT_BUILDER:?set the dedicated resource-limited builder name}"
: "${EPUSDT_IMAGE:?set a local image name tagged with the reviewed commit}"
root_dir=$(git rev-parse --show-toplevel)
cd "$root_dir"
if [ -n "$(git status --porcelain)" ]; then
    echo 'Refusing production build from a dirty checkout.' >&2
    exit 1
fi
revision=$(git rev-parse HEAD)
case "$EPUSDT_IMAGE" in *:"$revision") ;; *) echo 'Image tag must equal the complete reviewed Git commit.' >&2; exit 1;; esac
# Docker runtime cpus/mem_limit do not limit builds. See README for builder limits.
docker buildx build --builder "$EPUSDT_BUILDER" --load --file ops/secure/Dockerfile --build-arg "SOURCE_REVISION=$revision" --tag "$EPUSDT_IMAGE" .
docker image inspect "$EPUSDT_IMAGE" --format '{{.Id}} {{index .Config.Labels "org.opencontainers.image.revision"}}'
