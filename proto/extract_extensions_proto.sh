#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLED_CURSOR_DEFAULT="/Applications/Cursor.app/Contents/Resources/app/extensions/cursor-always-local/dist/main.js"
INPUT_DEFAULT="$INSTALLED_CURSOR_DEFAULT"
OUTPUT_DEFAULT="$SCRIPT_DIR/from_extensions"

INPUT_PATH="${1:-$INPUT_DEFAULT}"
OUTPUT_DIR="${2:-$OUTPUT_DEFAULT}"

# Resolve input: accept either a single JS file or an extensions root directory.
if [[ -d "$INPUT_PATH" ]]; then
  CANDIDATES=(
    "$INPUT_PATH/cursor-always-local/dist/main.js"
    "$INPUT_PATH/cursor-retrieval/dist/main.js"
    "$INPUT_PATH/dist/main.js"
  )
  FOUND_CANDIDATE=""
  for CANDIDATE in "${CANDIDATES[@]}"; do
    if [[ -f "$CANDIDATE" ]]; then
      FOUND_CANDIDATE="$CANDIDATE"
      break
    fi
  done
  if [[ -n "$FOUND_CANDIDATE" ]]; then
    INPUT_PATH="$FOUND_CANDIDATE"
  else
    mapfile -t JS_FILES < <(find "$INPUT_PATH" -type f -path "*/dist/main.js" | sort)
    if [[ ${#JS_FILES[@]} -eq 1 ]]; then
      INPUT_PATH="${JS_FILES[0]}"
    elif [[ ${#JS_FILES[@]} -eq 0 ]]; then
      echo "No dist/main.js found under: $INPUT_PATH" >&2
      exit 1
    else
      echo "Multiple dist/main.js files found under: $INPUT_PATH" >&2
      printf ' - %s\n' "${JS_FILES[@]}" >&2
      echo "Please pass a concrete input JS file as the first argument." >&2
      exit 1
    fi
  fi
fi

if [[ ! -f "$INPUT_PATH" ]]; then
  echo "Input JS not found: $INPUT_PATH" >&2
  echo "Install/update Cursor, or pass an explicit input bundle:" >&2
  echo "  $0 /path/to/cursor-always-local/dist/main.js [output-dir]" >&2
  exit 1
fi

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

go run "$SCRIPT_DIR/ext_tool" \
  -input "$INPUT_PATH" \
  -output "$OUTPUT_DIR" \
  -skip-format \
  -strict
