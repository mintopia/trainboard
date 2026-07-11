#!/usr/bin/env bash
# Publishes the baked image to Cloudflare R2 (S3 API). The bucket is
# SHARED with other projects: every operation below is scoped to the
# trainboard/ prefix — widening that scope is a bug, not a convenience.
set -euo pipefail

BUCKET=mintopia-github
PREFIX=trainboard
KEEP=5

usage() {
  echo "usage: $0 --tag vX.Y.Z --work DIR [--dry-run] [--list-file FILE]" >&2
  echo "  --list-file FILE   test seam: read the prune listing from FILE" >&2
  echo "                     instead of calling aws s3api. Only valid" >&2
  echo "                     together with --dry-run." >&2
  exit 2
}

TAG=""
WORK=""
DRY=""
LIST_FILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) [ $# -ge 2 ] || usage; TAG=$2; shift 2;;
    --work) [ $# -ge 2 ] || usage; WORK=$2; shift 2;;
    --dry-run) DRY=1; shift;;
    --list-file) [ $# -ge 2 ] || usage; LIST_FILE=$2; shift 2;;
    -h|--help) usage;;
    *) usage;;
  esac
done
[ -n "$TAG" ] && [ -n "$WORK" ] || usage

if [ -n "$LIST_FILE" ] && [ -z "$DRY" ]; then
  echo "--list-file is only valid together with --dry-run" >&2
  exit 2
fi

# Credentials are required for every real aws call. The one exemption:
# pure --dry-run --list-file, where the mutating calls are only echoed
# (run(), below) and the prune listing comes from a fixture file instead
# of a live `aws s3api` call — that combination never touches the network,
# so it's the offline-testable seam this script's tests rely on.
if ! { [ -n "$DRY" ] && [ -n "$LIST_FILE" ]; }; then
  missing=()
  [ -n "${R2_ACCOUNT_ID:-}" ]        || missing+=(R2_ACCOUNT_ID)
  [ -n "${R2_ACCESS_KEY_ID:-}" ]     || missing+=(R2_ACCESS_KEY_ID)
  [ -n "${R2_SECRET_ACCESS_KEY:-}" ] || missing+=(R2_SECRET_ACCESS_KEY)
  if [ "${#missing[@]}" -gt 0 ]; then
    echo "missing required env var(s): ${missing[*]}" >&2
    exit 1
  fi
fi

ENDPOINT="https://${R2_ACCOUNT_ID:-}.r2.cloudflarestorage.com"
export AWS_ACCESS_KEY_ID=${R2_ACCESS_KEY_ID:-} AWS_SECRET_ACCESS_KEY=${R2_SECRET_ACCESS_KEY:-}
AWS=(aws --endpoint-url "$ENDPOINT" --region auto)

run() { if [ -n "$DRY" ]; then echo "DRY: $*"; else "$@"; fi; }

# assert_scoped: every key headed for deletion must provably live under
# $PREFIX/ — this is the one thing standing between a bug here and
# clobbering another project's objects in the shared bucket.
assert_scoped() {
  case "$1" in
    "$PREFIX"/*) ;;
    *) echo "refusing to delete key outside $PREFIX/: $1" >&2; exit 1;;
  esac
}

IMG="trainboard-${TAG}.img.xz"
IMG_PATH="$WORK/$IMG"
SUM_PATH="$WORK/$IMG.sha256"

if [ -z "$LIST_FILE" ]; then
  [ -f "$IMG_PATH" ] || { echo "missing artifact: $IMG_PATH" >&2; exit 1; }
  [ -f "$SUM_PATH" ] || { echo "missing artifact: $SUM_PATH" >&2; exit 1; }
fi

run "${AWS[@]}" s3 cp "$IMG_PATH" "s3://$BUCKET/$PREFIX/$IMG"
run "${AWS[@]}" s3 cp "$SUM_PATH" "s3://$BUCKET/$PREFIX/$IMG.sha256"

# latest alias for the image: server-side copy AFTER the versioned upload
# succeeded. `aws s3 cp` (even with --copy-props none) sends an
# x-amz-tagging-directive header on the single-object CopyObject call it
# makes for small files — R2 rejects it as NotImplemented (confirmed live:
# the multipart copy path taken for the large image happened not to hit
# this, but a small-object copy did). The low-level `s3api copy-object`
# call maps straight to the CopyObject API with no tagging/ACL headers
# added, which R2 does support.
run "${AWS[@]}" s3api copy-object --copy-source "$BUCKET/$PREFIX/$IMG" --bucket "$BUCKET" --key "$PREFIX/trainboard-latest.img.xz"

# latest .sha256: REGENERATED with the latest filename, never copied — the
# versioned checksum line names trainboard-vX.Y.Z.img.xz, so a user who
# downloads the latest pair couldn't `sha256sum -c` it without renaming
# files (confirmed on the first real publish). Same hash (the alias is a
# byte-identical server-side copy of the versioned object), latest name.
# Written to a private mktemp dir, NOT $WORK: in CI the build runs under
# sudo so $WORK is root-owned, while this publish step is unprivileged.
LATEST_TMP=$(mktemp -d)
# Keep the generated file in --dry-run: it is part of the inspectable plan
# (the offline test reads it to verify the regenerated checksum line).
trap '[ -n "$DRY" ] || rm -rf "$LATEST_TMP"' EXIT
LATEST_SUM="$LATEST_TMP/trainboard-latest.img.xz.sha256"
if [ -f "$SUM_PATH" ]; then
  hash=$(cut -d' ' -f1 "$SUM_PATH")
  printf '%s  trainboard-latest.img.xz\n' "$hash" > "$LATEST_SUM"
fi
run "${AWS[@]}" s3 cp "$LATEST_SUM" "s3://$BUCKET/$PREFIX/trainboard-latest.img.xz.sha256"

# Prune: list ONLY trainboard/trainboard-v*.img.xz, sort by version, keep
# newest $KEEP, delete the rest (+ their .sha256). sort -V handles semver
# ordering; prerelease tags sort before their release, acceptable here.
if [ -n "$LIST_FILE" ]; then
  listing=$(cat "$LIST_FILE")
else
  listing=$("${AWS[@]}" s3api list-objects-v2 --bucket "$BUCKET" --prefix "$PREFIX/trainboard-v" \
    --query 'Contents[].Key' --output text | tr '\t' '\n')
fi

pattern="^${PREFIX}/trainboard-v[^/]+\\.img\\.xz\$"
# `|| true`: zero matches (e.g. a freshly bootstrapped prefix) is a valid
# state, not an error — without this, grep's exit 1 would trip
# `set -o pipefail` and abort the whole publish after the upload succeeded.
filtered=$(printf '%s\n' "$listing" | grep -E "$pattern" || true)

if [ -n "$filtered" ]; then
  # `head -n -N` (negative counts) is GNU-only and not available on BSD/macOS
  # head, which this script's own test suite runs under — awk keeps the
  # "all but the newest $KEEP" trim portable across both.
  stale=$(printf '%s\n' "$filtered" | sort -V \
    | awk -v keep="$KEEP" '{a[NR]=$0} END{for (i=1;i<=NR-keep;i++) print a[i]}')
  if [ -n "$stale" ]; then
    while IFS= read -r key; do
      [ -n "$key" ] || continue
      assert_scoped "$key"
      run "${AWS[@]}" s3 rm "s3://$BUCKET/$key"
      assert_scoped "$key.sha256"
      run "${AWS[@]}" s3 rm "s3://$BUCKET/$key.sha256"
    done <<EOF
$stale
EOF
  fi
fi
