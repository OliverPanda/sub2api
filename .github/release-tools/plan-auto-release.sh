#!/usr/bin/env bash
set -euo pipefail

: "${EVENT_NAME:?}" "${TESTED_SHA:?}" "${DEFAULT_BRANCH:?}" "${GITHUB_OUTPUT:?}"

git fetch --no-tags origin "+refs/heads/${DEFAULT_BRANCH}:refs/remotes/origin/${DEFAULT_BRANCH}"
head_sha=$(git rev-parse HEAD)
remote_sha=$(git rev-parse "origin/${DEFAULT_BRANCH}")

if [[ "$EVENT_NAME" == "workflow_run" && "$remote_sha" != "$TESTED_SHA" ]]; then
  echo "::notice::Skipping stale CI result ${TESTED_SHA}; ${DEFAULT_BRANCH} is ${remote_sha}"
  echo "release=false" >> "$GITHUB_OUTPUT"
  exit 0
fi
if [[ "$EVENT_NAME" == "workflow_run" && "$head_sha" != "$TESTED_SHA" ]]; then
  echo "::notice::Checked-out HEAD ${head_sha} does not match tested SHA ${TESTED_SHA}"
  echo "release=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

latest_tag=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-version:refname | head -n 1)
if [[ -z "$latest_tag" ]]; then
  latest_tag=v0.0.0
fi

if [[ "$head_sha" == "$(git rev-parse "${latest_tag}^{commit}")" ]]; then
  echo "::notice::HEAD is already released as ${latest_tag}"
  echo "release=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

changed=$(git diff --name-only "${latest_tag}..HEAD")
app_changed=false
while IFS= read -r path; do
  case "$path" in
    backend/*|frontend/*|bridge/*|Dockerfile.goreleaser|deploy/docker-entrypoint.sh|.goreleaser.yaml|.goreleaser.simple.yaml)
      app_changed=true
      break
      ;;
  esac
done <<< "$changed"
if [[ "$app_changed" != true ]]; then
  echo "::notice::No application changes since ${latest_tag}; skipping release"
  echo "release=false" >> "$GITHUB_OUTPUT"
  exit 0
fi

version=${latest_tag#v}
IFS=. read -r major minor patch <<< "$version"
[[ "$major" =~ ^[0-9]+$ && "$minor" =~ ^[0-9]+$ && "$patch" =~ ^[0-9]+$ ]] || {
  echo "Invalid latest release tag: ${latest_tag}" >&2
  exit 1
}
next_version="${major}.${minor}.$((patch + 1))"
next_tag="v${next_version}"
if git rev-parse -q --verify "refs/tags/${next_tag}" >/dev/null; then
  echo "Release tag ${next_tag} already exists" >&2
  exit 1
fi

notes=$(git log --no-merges --pretty='- %s' "${latest_tag}..HEAD" | head -n 40)
{
  echo "release=true"
  echo "version=${next_version}"
  echo "tag=${next_tag}"
  echo "previous_tag=${latest_tag}"
  echo "notes<<AUTO_RELEASE_NOTES"
  printf '%s\n' "$notes"
  echo "AUTO_RELEASE_NOTES"
} >> "$GITHUB_OUTPUT"
