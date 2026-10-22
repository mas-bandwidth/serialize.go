#!/bin/sh
# tools/revendor.sh <tag>
#
# Re-vendors the normative files from mas-bandwidth/serialize at a released tag:
# STANDARD.md, the conformance/ corpus and the conformance-controls/ lists, copied
# verbatim, then prints the diff against what was vendored before. Run it from the
# repository root, read the diff, port whatever it implies, and commit the files in
# the same change. The spec-sync and corpus-sync CI jobs fail when the copies drift.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: tools/revendor.sh <tag>" >&2
	exit 2
fi
tag=$1

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

git -c advice.detachedHead=false clone --quiet --depth 1 --branch "$tag" https://github.com/mas-bandwidth/serialize.git "$scratch/serialize"
echo "upstream: mas-bandwidth/serialize $tag @ $(git -C "$scratch/serialize" rev-parse HEAD)"

cp "$scratch/serialize/STANDARD.md" STANDARD.md
rm -rf conformance conformance-controls
cp -R "$scratch/serialize/conformance" conformance
cp -R "$scratch/serialize/conformance-controls" conformance-controls

git status --short -- STANDARD.md conformance conformance-controls
git --no-pager diff --stat -- STANDARD.md conformance conformance-controls
