#!/usr/bin/env bash
# Track O: the pre-commit gate. This file is the single definition of "green" —
# CI calls it, developers run it before committing, so the two can never drift
# apart. Fails on the first red gate. Secret scanning lives in Track P's
# policy test (allow-listed), not in this grep-free script.
#
#   bash scripts/verify.sh              # local: tests without -race
#   VERIFY_RACE=1 bash scripts/verify.sh  # CI: tests with -race (needs cgo)
set -u

step() {
  local name="$1"
  shift
  if "$@" > /tmp/verify-agentops.log 2>&1; then
    echo "ok   $name"
  else
    echo "FAIL $name"
    cat /tmp/verify-agentops.log
    exit 1
  fi
}

RACE=""
if [ "${VERIFY_RACE:-0}" = "1" ]; then
  RACE="-race"
fi

step "gofmt" bash -c 'test -z "$(gofmt -l .)"'
step "vet" go vet ./...
# shellcheck disable=SC2086
step "test" go test $RACE -count=1 ./...
step "build" go build ./...
step "policy" go test -count=1 ./policy/
step "diffstat" git diff --stat

echo "green: the tree is committable"
