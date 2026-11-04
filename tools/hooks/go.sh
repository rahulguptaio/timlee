#!/usr/bin/env bash
# Go checks for the git hooks, each limited to what the commit or push
# changes. CI runs the whole-module versions (gofmt -l ., go vet ./...,
# go test ./... in .github/workflows/ci.yml), so nothing here is stricter.
#
#   tools/hooks/go.sh fmt <file>...  fail if gofmt would rewrite a staged file
#   tools/hooks/go.sh vet            go vet the packages with staged Go changes
#   tools/hooks/go.sh test           go test -short the packages with staged Go changes
#   tools/hooks/go.sh test <ref>     go test -short the packages changed since <ref>
set -euo pipefail

usage() {
  echo "usage: $0 fmt <file>... | vet | test [<git-ref>]" >&2
  exit 64
}

# packages reads Go file paths on stdin and prints one pattern per package,
# skipping what `./...` skips: testdata, vendor, names starting with . or _,
# and directories that belong to a nested module.
packages() {
  local skip='/(testdata|vendor|[._][^/]+)/' file dir mod
  while IFS= read -r file; do
    if [[ $file == */* ]]; then echo "${file%/*}"; else echo .; fi
  done | sort -u | while IFS= read -r dir; do
    [[ -d $dir && ! /$dir/ =~ $skip ]] || continue
    mod=$dir
    while [[ $mod != . && ! -f $mod/go.mod ]]; do
      if [[ $mod == */* ]]; then mod=${mod%/*}; else mod=.; fi
    done
    [[ $mod == . ]] || continue
    if [[ $dir == . ]]; then echo .; else echo "./$dir"; fi
  done
}

fmt() {
  local gofmt out
  [[ $# -gt 0 ]] || return 0
  gofmt=$(command -v gofmt || echo "$(go env GOROOT)/bin/gofmt")
  out=$("$gofmt" -l "$@")
  [[ -n $out ]] || return 0
  echo "gofmt would rewrite these staged files:"
  echo "$out"
  echo "Fix: gofmt -w <file> && git add <file>"
  return 1
}

vet() {
  local pkgs
  pkgs=$(git diff --cached --name-only --diff-filter=ACMRD -- '*.go' | packages)
  if [[ -z $pkgs ]]; then
    echo "go vet: no staged changes in Go packages."
    return 0
  fi
  # shellcheck disable=SC2086 # one pattern per package; import paths have no spaces
  go vet $pkgs
}

test_changed() {
  local pkgs
  pkgs=$(git diff --name-only --diff-filter=ACMRD "$1"...HEAD -- '*.go' | packages)
  if [[ -z $pkgs ]]; then
    echo "go test: no Go packages changed since $1."
    return 0
  fi
  # shellcheck disable=SC2086 # one pattern per package; import paths have no spaces
  go test -short $pkgs
}

test_staged() {
  local pkgs
  pkgs=$(git diff --cached --name-only --diff-filter=ACMRD -- '*.go' | packages)
  if [[ -z $pkgs ]]; then
    echo "go test: no staged changes in Go packages."
    return 0
  fi
  # shellcheck disable=SC2086 # one pattern per package; import paths have no spaces
  go test -short $pkgs
}

case ${1:-} in
  fmt)
    shift
    fmt "$@"
    ;;
  vet)
    [[ $# -eq 1 ]] || usage
    vet
    ;;
  test)
    case $# in
      1) test_staged ;;
      2) test_changed "$2" ;;
      *) usage ;;
    esac
    ;;
  *) usage ;;
esac
