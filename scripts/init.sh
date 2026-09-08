#!/usr/bin/env bash
#
# init.sh — bootstrap a new MCP server from this template.
#
# Replaces every placeholder ({MCP-NAME}, mcp-template, internal/example,
# team-PLACEHOLDER, SHORT_DESC_PLACEHOLDER, default port) with values you
# pass on the command line, then deletes itself + the bootstrap-gate
# workflow and commits.
#
# Usage:
#   ./scripts/init.sh \
#     --name=mcp-foo \
#     --team=team-atlas \
#     --module=github.com/giantswarm/mcp-foo \
#     --short-desc='MCP server for Foo' \
#     [--audience=mcp-foo] \
#     [--port=8080] \
#     [--domain=foo]

set -euo pipefail

NAME=""
TEAM=""
MODULE=""
SHORT_DESC=""
AUDIENCE=""
PORT="8080"
DOMAIN=""

usage() {
  sed -n '2,20p' "$0"
  exit 1
}

for arg in "$@"; do
  case "$arg" in
    --name=*)        NAME="${arg#*=}" ;;
    --team=*)        TEAM="${arg#*=}" ;;
    --module=*)      MODULE="${arg#*=}" ;;
    --short-desc=*)  SHORT_DESC="${arg#*=}" ;;
    --audience=*)    AUDIENCE="${arg#*=}" ;;
    --port=*)        PORT="${arg#*=}" ;;
    --domain=*)      DOMAIN="${arg#*=}" ;;
    -h|--help)       usage ;;
    *) echo "unknown flag: $arg" >&2; usage ;;
  esac
done

[[ -z "$NAME"       ]] && { echo "--name is required"       >&2; exit 1; }
[[ -z "$TEAM"       ]] && { echo "--team is required"       >&2; exit 1; }
[[ -z "$MODULE"     ]] && { echo "--module is required"     >&2; exit 1; }
[[ -z "$SHORT_DESC" ]] && { echo "--short-desc is required" >&2; exit 1; }

# Defaults derived from --name when not given.
[[ -z "$AUDIENCE" ]] && AUDIENCE="$NAME"
# Strip a leading mcp- to get a default domain noun ("mcp-foo" -> "foo").
[[ -z "$DOMAIN" ]] && DOMAIN="${NAME#mcp-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ ! -f scripts/init.sh ]]; then
  echo "must be run from the template root (scripts/init.sh missing)" >&2
  exit 1
fi

echo "==> bootstrapping $NAME"
echo "    module:    $MODULE"
echo "    team:      $TEAM"
echo "    audience:  $AUDIENCE"
echo "    port:      $PORT"
echo "    domain:    $DOMAIN"

# Files to skip during string substitution (binaries, lockfiles).
SKIP_RE='\.(png|jpg|jpeg|gif|ico|woff2?|ttf|otf)$|^go\.sum$|/build/'

# Substitute strings across all text files.
substitute() {
  local from="$1" to="$2"
  # shellcheck disable=SC2016
  git ls-files | grep -Ev "$SKIP_RE" | while read -r f; do
    if [[ -f "$f" ]] && grep -qF "$from" "$f"; then
      # Use a sentinel char absent from inputs (\x01) as sed delimiter.
      sed -i $'s\x01'"$from"$'\x01'"$to"$'\x01g' "$f"
    fi
  done
}

# Order matters: replace the longer / more-specific tokens first.
substitute 'github.com/giantswarm/mcp-template' "$MODULE"
substitute 'gsoci.azurecr.io/giantswarm/mcp-template' "gsoci.azurecr.io/giantswarm/$NAME"
substitute 'mcp-template' "$NAME"
substitute '{MCP-NAME}' "$NAME"
substitute 'mcp-template-audience' "$AUDIENCE"
substitute "${NAME}-audience" "$AUDIENCE"  # values.yaml default; idempotent
substitute 'team-PLACEHOLDER' "$TEAM"
substitute 'SHORT_DESC_PLACEHOLDER' "$SHORT_DESC"
substitute '"8080"' "\"$PORT\""

# Rename directories last so the substitutions above hit the right files.
[[ -d 'helm/{MCP-NAME}'    ]] && mv 'helm/{MCP-NAME}'    "helm/$NAME"
[[ -d 'internal/example'   ]] && mv 'internal/example'   "internal/$DOMAIN"

# Substitute the example domain package name in Go imports + identifiers.
if [[ "$DOMAIN" != "example" ]]; then
  substitute 'internal/example' "internal/$DOMAIN"
  substitute '"$MODULE"/internal/example' "\"$MODULE\"/internal/$DOMAIN"
  # Package name in source files.
  find "internal/$DOMAIN" -name '*.go' -print0 | xargs -0 sed -i "s/^package example\$/package $DOMAIN/"
  substitute 'example.NewFakeClient' "$DOMAIN.NewFakeClient"
  substitute 'example.Client' "$DOMAIN.Client"
  substitute 'example.Thing' "$DOMAIN.Thing"
  substitute 'example.ErrNotFound' "$DOMAIN.ErrNotFound"
fi

# Drop template-only artefacts.
rm -f .github/workflows/template-bootstrap-gate.yaml
rm -f scripts/init.sh

# Refresh module graph and stage everything.
go mod tidy

echo
echo "==> done. Review the diff, then:"
echo "      git add -A && git commit -m 'chore: bootstrap from mcp-template'"
