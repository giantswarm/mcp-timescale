#!/usr/bin/env bash
# Render the chart the way app-build-suite renders a branch build and assert
# that every label value and object name the chart derives from a 63-character
# cut stays valid (giantswarm/mcp-timescale#7).
#
# Branch builds version the chart <semver>-dev.<branch>.<YYYY-MM-DD>.<HH-MM-SS>.h<sha7>.
# helm.sh/chart is "<name>-<version>" cut to 63 characters; for one branch-name
# length per dot in the version the 63rd character is that dot, and a label
# value ending on "." is rejected by the API server for every object of the
# release. The breaking lengths are computed here from the actual chart name
# and base version, so the check keeps hitting all three dots when either
# changes. The exact version from the issue, a build-metadata version ("+" is
# rewritten to "_") and a plain release version round the set off.
#
# Every version is packaged (helm package --version, as app-build-suite does)
# and rendered with every helm/<chart>/ci/*-values.yaml case and with the ATS
# install values; the ci/long-names-values.yaml case adds name and fullname
# overrides whose 63rd character is a dot.
#
# Usage: scripts/verify-chart-labels.sh   (or: make helm-verify-labels)
# Env:   CHART_DIR (default helm/mcp-timescale), HELM (default helm),
#        BASE_VERSION (default 0.1.1), RELEASE (default mcp-timescale),
#        NAMESPACE (default mcp-timescale), OUT (default: a fresh temp dir).
set -euo pipefail

CHART_DIR=${CHART_DIR:-helm/mcp-timescale}
HELM=${HELM:-helm}
BASE_VERSION=${BASE_VERSION:-0.1.1}
RELEASE=${RELEASE:-mcp-timescale}
NAMESPACE=${NAMESPACE:-mcp-timescale}
OUT=${OUT:-$(mktemp -d)}

# Label values: 63 characters at most, alphanumeric at both ends.
label_re='^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$'
# Object names: RFC 1123 subdomain, alphanumeric at both ends of every segment.
name_re='^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$'

chart_name=$(sed -nE 's/^name:[[:space:]]*"?([^"]*)"?[[:space:]]*$/\1/p' "$CHART_DIR/Chart.yaml")
[ -n "$chart_name" ] || { echo "FAIL: cannot read the chart name from $CHART_DIR/Chart.yaml" >&2; exit 1; }

# A long, realistic branch name cut to exactly $1 characters (never ending on "-").
branch_of_length() {
  local want=$1 long=a-very-long-branch-name-that-runs-well-past-the-sixty-three-character-cut-of-the-label
  local b=${long:0:$want}
  b=${b%-}
  while [ ${#b} -lt "$want" ]; do b="${b}x"; done
  printf '%s' "$b"
}

versions=()
prefix="${chart_name}-${BASE_VERSION}-dev."
# Offset of each dot after the branch name: ".YYYY-MM-DD" (1), ".HH-MM-SS" (1+10+1), ".h<sha7>" (1+10+1+8+1).
for offset in 1 12 21; do
  len=$((63 - ${#prefix} - offset))
  if [ "$len" -ge 1 ]; then
    versions+=("${BASE_VERSION}-dev.$(branch_of_length "$len").2026-09-08.14-11-03.h1234567")
  else
    echo "skip: no branch length puts a dot at character 63 for offset $offset (prefix '$prefix' is too long)"
  fi
done
versions+=(
  "0.1.1-dev.a-very-long-branch-name.2026-09-08.14-11-03.h1234567"
  "${BASE_VERSION}+h1234567"
  "0.2.0"
)

values_files=()
for f in "$CHART_DIR"/ci/*-values.yaml tests/test-values.yaml; do
  [ -f "$f" ] && values_files+=("$f")
done
[ ${#values_files[@]} -gt 0 ] || { echo "FAIL: no values files found under $CHART_DIR/ci" >&2; exit 1; }

fail=0
i=0
for version in "${versions[@]}"; do
  i=$((i + 1))
  pkg="$OUT/pkg-$i"
  mkdir -p "$pkg"
  "$HELM" package "$CHART_DIR" --version "$version" -d "$pkg" >/dev/null
  tgz=$(find "$pkg" -name '*.tgz' | head -n1)
  [ -n "$tgz" ] || { echo "FAIL: helm package produced no archive for version $version" >&2; exit 1; }

  for values in "${values_files[@]}"; do
    case=$(basename "$values" .yaml)
    render="$pkg/$case.yaml"
    "$HELM" template "$RELEASE" "$tgz" -n "$NAMESPACE" -f "$values" > "$render"

    # Every helm.sh/chart and app.kubernetes.io/name value, at any indentation
    # (pod templates included), quoted or not.
    mapfile -t labels < <(sed -nE 's#^[[:space:]]*(helm\.sh/chart|app\.kubernetes\.io/name):[[:space:]]*"?([^"]*)"?[[:space:]]*$#\2#p' "$render" | sort -u)
    chart_label=$(sed -nE 's#^[[:space:]]*helm\.sh/chart:[[:space:]]*"?([^"]*)"?[[:space:]]*$#\1#p' "$render" | sort -u | tr '\n' ' ')
    if [ ${#labels[@]} -eq 0 ]; then
      echo "FAIL: $case @ $version rendered no helm.sh/chart or app.kubernetes.io/name label"
      fail=1
      continue
    fi
    for l in "${labels[@]}"; do
      if [ ${#l} -gt 63 ] || ! [[ $l =~ $label_re ]]; then
        echo "FAIL: $case @ $version: label value '$l' is not a valid label value (${#l} characters)"
        fail=1
      fi
    done

    # metadata.name of every top-level object.
    mapfile -t names < <(awk '
      /^[^[:space:]]/ { in_meta = ($0 == "metadata:") ; next }
      in_meta && /^  name:/ { sub(/^  name:[[:space:]]*/, ""); gsub(/"/, ""); print }
    ' "$render" | sort -u)
    if [ ${#names[@]} -eq 0 ]; then
      echo "FAIL: $case @ $version rendered no top-level metadata.name"
      fail=1
      continue
    fi
    for n in "${names[@]}"; do
      if [ ${#n} -gt 253 ] || ! [[ $n =~ $name_re ]]; then
        echo "FAIL: $case @ $version: metadata.name '$n' is not a valid object name (${#n} characters)"
        fail=1
      fi
    done

    echo "ok: $case @ $version -> helm.sh/chart=${chart_label% }"
  done
done

if [ "$fail" -ne 0 ]; then
  echo "FAIL: chart label/name check failed; renders are under $OUT" >&2
  exit 1
fi
echo "ok: every helm.sh/chart and app.kubernetes.io/name value and every metadata.name is valid for ${#versions[@]} versions x ${#values_files[@]} values files"
