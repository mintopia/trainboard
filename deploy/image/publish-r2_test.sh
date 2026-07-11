#!/usr/bin/env bash
# Offline test for publish-r2.sh's --dry-run --list-file seam: exercises
# arg parsing, the dry-run command printing, and the prune sort/trim logic
# against a fixture listing, without an aws binary or real R2 credentials
# (deploy.md-style DESTDIR test, see install-trainboard_test.sh).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
unset R2_ACCOUNT_ID R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY 2>/dev/null || true

fail() { echo "FAIL: $1" >&2; exit 1; }

# Fixture: 7 versioned images in mixed order — v0.9.0 vs v0.10.0 is the
# sort -V trap (naive string sort would rank v0.10.0 before v0.9.0) — plus
# the latest alias and a foreign key belonging to another project that
# shares this bucket. .sha256 companions are listed too, exactly as a real
# S3 listing would include them, so the filter must exclude them (the
# script re-derives each surviving key's .sha256 by appending the suffix,
# never by matching it directly).
cat > "$T/listing.txt" <<'EOF'
trainboard/trainboard-v0.9.0.img.xz
trainboard/trainboard-v0.9.0.img.xz.sha256
trainboard/trainboard-v0.10.0.img.xz
trainboard/trainboard-v0.10.0.img.xz.sha256
trainboard/trainboard-v0.2.0.img.xz
trainboard/trainboard-v0.2.0.img.xz.sha256
trainboard/trainboard-v0.11.0.img.xz
trainboard/trainboard-v0.11.0.img.xz.sha256
trainboard/trainboard-v0.1.0.img.xz
trainboard/trainboard-v0.1.0.img.xz.sha256
trainboard/trainboard-v0.5.0.img.xz
trainboard/trainboard-v0.5.0.img.xz.sha256
trainboard/trainboard-v0.3.0.img.xz
trainboard/trainboard-v0.3.0.img.xz.sha256
trainboard/trainboard-latest.img.xz
trainboard/trainboard-latest.img.xz.sha256
otherproject/file
EOF

TAG=v0.12.0
IMG="trainboard-${TAG}.img.xz"
# ENDPOINT is deterministic here: R2_ACCOUNT_ID is unset (unnecessary for
# this credential-free seam), so the script builds "https://.r2...".
EP="https://.r2.cloudflarestorage.com"
CP="DRY: aws --endpoint-url $EP --region auto s3 cp"
RM="DRY: aws --endpoint-url $EP --region auto s3 rm"

OUT=$("$HERE/publish-r2.sh" --tag "$TAG" --work "$T" --dry-run --list-file "$T/listing.txt")
echo "$OUT"

echo "=== Checking versioned upload (2 objects) ==="
grep -qF "$CP $T/$IMG s3://mintopia-github/trainboard/$IMG" <<<"$OUT" || fail "missing versioned image upload"
grep -qF "$CP $T/$IMG.sha256 s3://mintopia-github/trainboard/$IMG.sha256" <<<"$OUT" || fail "missing versioned sha256 upload"
[ "$(grep -c "^$CP $T/" <<<"$OUT")" -eq 2 ] || fail "expected exactly 2 versioned uploads"

echo "=== Checking latest-alias copy (2 objects) ==="
grep -qF "$CP s3://mintopia-github/trainboard/$IMG s3://mintopia-github/trainboard/trainboard-latest.img.xz" <<<"$OUT" || fail "missing latest alias copy"
grep -qF "$CP s3://mintopia-github/trainboard/$IMG.sha256 s3://mintopia-github/trainboard/trainboard-latest.img.xz.sha256" <<<"$OUT" || fail "missing latest alias sha256 copy"
[ "$(grep -c "^$CP s3://" <<<"$OUT")" -eq 2 ] || fail "expected exactly 2 alias copies"

echo "=== Checking prune deletes exactly the 2 oldest versions (4 keys) ==="
deletes=$(grep "^$RM " <<<"$OUT" || true)
[ "$(printf '%s\n' "$deletes" | grep -c .)" -eq 4 ] || fail "expected 4 deletions, got: $deletes"
grep -qF "$RM s3://mintopia-github/trainboard/trainboard-v0.1.0.img.xz" <<<"$deletes" || fail "did not delete v0.1.0 image"
grep -qF "$RM s3://mintopia-github/trainboard/trainboard-v0.1.0.img.xz.sha256" <<<"$deletes" || fail "did not delete v0.1.0 sha256"
grep -qF "$RM s3://mintopia-github/trainboard/trainboard-v0.2.0.img.xz" <<<"$deletes" || fail "did not delete v0.2.0 image"
grep -qF "$RM s3://mintopia-github/trainboard/trainboard-v0.2.0.img.xz.sha256" <<<"$deletes" || fail "did not delete v0.2.0 sha256"

echo "=== Checking the alias and foreign keys are never touched ==="
if grep -q 'trainboard-latest' <<<"$deletes"; then fail "must never delete the latest alias"; fi
if grep -q 'otherproject' <<<"$OUT"; then fail "otherproject/file must never appear in the plan"; fi

echo "=== Checking missing env vars are rejected without --list-file ==="
if "$HERE/publish-r2.sh" --tag "$TAG" --work "$T" --dry-run 2>"$T/err.txt"; then
  fail "expected failure: --dry-run without --list-file needs real creds to list"
fi
grep -q 'R2_ACCOUNT_ID' "$T/err.txt" || fail "missing-env error did not name R2_ACCOUNT_ID"

echo "=== Checking --list-file without --dry-run is rejected ==="
if "$HERE/publish-r2.sh" --tag "$TAG" --work "$T" --list-file "$T/listing.txt" 2>"$T/err2.txt"; then
  fail "expected failure: --list-file requires --dry-run"
fi
grep -q -- '--list-file' "$T/err2.txt" || fail "list-file-without-dry-run error message unclear"

echo "=== Checking missing --tag/--work triggers usage ==="
if "$HERE/publish-r2.sh" --dry-run 2>"$T/err3.txt"; then
  fail "expected failure with missing --tag/--work"
fi
grep -q 'usage:' "$T/err3.txt" || fail "missing-args error did not print usage"

echo "=== Checking a real deletion path outside trainboard/ would be refused ==="
cat > "$T/foreign-listing.txt" <<'EOF'
otherproject/trainboard-v0.1.0.img.xz
EOF
# assert_scoped guards every deletion; nothing here should match the
# trainboard-v*.img.xz filter in the first place (it's prefixed
# otherproject/, not trainboard/), so the plan must contain no deletions.
OUT2=$("$HERE/publish-r2.sh" --tag "$TAG" --work "$T" --dry-run --list-file "$T/foreign-listing.txt")
if grep -q "^$RM " <<<"$OUT2"; then fail "a foreign-prefixed key must never reach a delete command"; fi

echo OK
