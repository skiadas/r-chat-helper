#!/usr/bin/env sh
# Deploy update for r-chat-helper: pull a new image from GHCR and restart the
# app only when the image digest changed. Meant to run from a cron job; it
# reads GHCR_OWNER and IMAGE_TAG from the .env in this directory.
set -eu

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

# shellcheck disable=SC1091
. ./.env

IMAGE="ghcr.io/${GHCR_OWNER}/r-chat-helper"
TAG="${IMAGE_TAG:-latest}"

before="$(docker inspect --format='{{index .RepoDigests 0}}' "${IMAGE}:${TAG}" 2>/dev/null || echo none)"

docker compose pull --quiet

after="$(docker inspect --format='{{index .RepoDigests 0}}' "${IMAGE}:${TAG}" 2>/dev/null || echo none)"

if [ "$before" != "$after" ]; then
  docker compose up -d
  echo "r-chat-helper: updated ${IMAGE}:${TAG} (${before} -> ${after})"
else
  echo "r-chat-helper: no change for ${IMAGE}:${TAG}"
fi