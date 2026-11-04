#!/usr/bin/env bash
# Commit messages follow Conventional Commits with a header of at most 72
# characters, and a blank line between the header and any body:
#   <type>(<scope>)!: <description>
#
#   scripts/checks/commit-msg.sh <message-file>   the commit-msg hook (lefthook.yml)
#   scripts/checks/commit-msg.sh --changed <ref>  every commit in <ref>..HEAD (CI)
#
# Accepted as git writes them: merges, `Revert "…"`/`Reapply "…"`, and
# fixup!/squash!/amend! commits. Trailers such as Co-authored-by are not
# checked. --changed skips commits authored by bots, and commits whose tree
# predates this script, since published history cannot be reworded.
# Exit 0 pass, 1 fail.
set -euo pipefail
export LC_ALL=C.UTF-8 # count characters, not bytes

max=72
types='build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test'

usage() {
  echo "usage: $0 <message-file> | --changed <git-ref>" >&2
  exit 64
}

# problem MESSAGE: succeed silently, or print what is wrong and fail.
problem() {
  local msg header rest second
  local re="^($types)(\\([^()[:space:]]+\\))?!?: [^[:space:]]"
  msg=$(sed '/[^[:space:]]/,$!d' <<<"$1")
  header=${msg%%$'\n'*}
  rest=${msg#"$header"}
  rest=${rest#$'\n'}
  second=${rest%%$'\n'*}

  case $header in
    'Merge '* | 'Revert "'* | 'Reapply "'* | 'fixup! '* | 'squash! '* | 'amend! '*) return 0 ;;
  esac
  if [[ -z $header ]]; then
    echo "the message is empty"
  elif ! [[ $header =~ $re ]]; then
    echo "the header is not <type>(<scope>): <description>"
  elif [[ ${#header} -gt $max ]]; then
    echo "the header is ${#header} characters; the limit is $max"
  elif [[ $second =~ [^[:space:]] ]]; then
    echo "the header must be followed by a blank line"
  else
    return 0
  fi
  return 1
}

hook() {
  local file=$1 c msg reason
  [[ -f $file ]] || usage
  c=$(git config core.commentChar || true)
  [[ ${#c} -eq 1 ]] || c='#'
  # git hands the hook the raw message: drop its comments, and everything
  # below the scissors line that `git commit -v` adds.
  msg=$(awk -v c="$c" '
    index($0, c " ------------------------ >8 ------------------------") == 1 { exit }
    substr($0, 1, 1) != c' "$file")
  if reason=$(problem "$msg"); then
    echo "commit-msg: ok"
    return 0
  fi
  file=$(cd "$(dirname "$file")" && pwd -P)/${file##*/}
  cat <<EOF
Commit message rejected: $reason.
  ${msg%%$'\n'*}

Use Conventional Commits with a header of at most $max characters:
  <type>(<scope>): <imperative description>
  e.g. fix(auth): reject expired invitation links
  types: ${types//|/ }
Your message is kept in $file; retry with: git commit -e -F $file
commit-msg: rejected
EOF
  return 1
}

changed() {
  local ref=$1 sha reason checked=0 failed=0
  if ! git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
    echo "commit-msg: $ref is not a commit" >&2
    exit 64
  fi
  for sha in $(git rev-list --reverse "$ref..HEAD"); do
    [[ $(git log -1 --format=%an "$sha") != *'[bot]' ]] || continue
    [[ $(git rev-list --parents -n 1 "$sha" | wc -w) -le 2 ]] || continue
    git cat-file -e "$sha:scripts/checks/commit-msg.sh" 2>/dev/null || continue
    checked=$((checked + 1))
    if ! reason=$(problem "$(git log -1 --format=%B "$sha")"); then
      failed=$((failed + 1))
      echo "✗ $(git log -1 --format='%h %s' "$sha")"
      echo "  $reason"
    fi
  done
  echo "commit-msg: $checked commit(s) checked, $failed rejected"
  [[ $failed -eq 0 ]]
}

case ${1:-} in
  --changed)
    [[ $# -eq 2 ]] || usage
    changed "$2"
    ;;
  -* | '') usage ;;
  *)
    [[ $# -eq 1 ]] || usage
    hook "$1"
    ;;
esac
