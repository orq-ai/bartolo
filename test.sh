#!/bin/sh

set -e

TEST_ARGS="$(printf '%s ' "$@")"
exec make verify TEST_ARGS="$TEST_ARGS"
