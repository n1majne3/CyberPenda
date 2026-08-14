#!/bin/bash
# One-shot PDF malware triage. Accepts Windows (D:\x\y.pdf) or WSL (/mnt/d/x/y.pdf) paths.
# Usage: scan.sh [--text] <file> [<file>...]
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEXT_FLAG=""
[ "$1" = "--text" ] && { TEXT_FLAG="--text"; shift; }
[ $# -eq 0 ] && { echo "Usage: scan.sh [--text] <file> [<file>...]"; exit 1; }

for RAW in "$@"; do
  case "$RAW" in
    [A-Za-z]:*) F="$(wslpath -u "$RAW")" ;;
    *)          F="$RAW" ;;
  esac
  echo "======================================================================"
  echo "== $F"
  if [ ! -f "$F" ]; then echo "[!] file not found"; continue; fi

  echo "---- fingerprint ----"
  ls -la "$F"
  file "$F"
  md5sum "$F"
  sha256sum "$F"

  echo "---- pdfid ----"
  if command -v pdfid >/dev/null 2>&1; then
    pdfid "$F"
  else
    echo "(pdfid not installed - relying on python triage below)"
  fi

  echo "---- python triage ----"
  python3 "$SCRIPT_DIR/pdf_triage.py" "$F" $TEXT_FLAG
  echo
done
exit 0
