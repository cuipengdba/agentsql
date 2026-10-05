#!/bin/sh
set -eu

case "${1:-gate}" in
  short)
    go test -short ./internal/parser -count=1
    ;;
  gate)
    # The test prints each round's P50/P90/P95/P99 and exits nonzero if any
    # P99 exceeds 5 ms. Run on an otherwise idle, fixed Linux runner.
    go test ./internal/parser -run '^TestParseProjectionLineageP99Budget$/^postgres$' -count=5 -v
    ;;
  bench)
    go test ./internal/parser -run '^$' -bench '^BenchmarkPostgres(ParseCorpus|TypicalStages)$' -benchmem -benchtime=1s -count=3
    ;;
  profile)
    profile_dir="${TMPDIR:-/tmp}"
    go test ./internal/parser -run '^$' -bench '^BenchmarkPostgresTypicalStages$/^full_parse$' \
      -benchtime=5s -o "$profile_dir/parser.test" \
      -cpuprofile="$profile_dir/parser-cpu.prof" \
      -memprofile="$profile_dir/parser-mem.prof"
    go tool pprof -top -nodecount=15 "$profile_dir/parser.test" "$profile_dir/parser-cpu.prof"
    go tool pprof -top -nodecount=15 -alloc_space "$profile_dir/parser.test" "$profile_dir/parser-mem.prof"
    ;;
  all)
    "$0" short
    "$0" gate
    "$0" bench
    "$0" profile
    ;;
  *)
    echo "usage: $0 [short|gate|bench|profile|all]" >&2
    exit 2
    ;;
esac
