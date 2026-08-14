#!/bin/bash
# Deep-dive flagged PDF objects: dump decoded streams + grep threat patterns.
# Usage: deep_dive.sh <file> <objnum> [objnum...]
#        deep_dive.sh <file> --auto     (auto-locate JS/XFA/EmbeddedFile objects via pdf-parser)
RAW="$1"; shift
case "$RAW" in
  [A-Za-z]:*) F="$(wslpath -u "$RAW")" ;;
  *)          F="$RAW" ;;
esac
[ ! -f "$F" ] && { echo "[!] file not found: $F"; exit 1; }
command -v pdf-parser >/dev/null 2>&1 || { echo "[!] pdf-parser not installed"; exit 1; }

OUT=/tmp/pdfscan
rm -rf "$OUT"; mkdir -p "$OUT"

OBJS="$*"
if [ "$1" = "--auto" ]; then
  echo "---- locating objects flagged for /JS /JavaScript /XFA /EmbeddedFile /OpenAction /AA /Launch ----"
  STATS=$(pdf-parser -a "$F" 2>/dev/null)
  echo "$STATS" | grep -E '^\s*/(JS|JavaScript|XFA|EmbeddedFile|OpenAction|AA|Launch|RichMedia|SubmitForm)\b'
  OBJS=$(echo "$STATS" | grep -E '^\s*/(JS|JavaScript|XFA|EmbeddedFile|OpenAction|AA|Launch|RichMedia|SubmitForm)\b' \
        | sed 's/.*: //' | tr ',' '\n' | tr -d ' ' | sort -un | tr '\n' ' ')
  echo "objects to dump: $OBJS"
fi

for o in $OBJS; do
  echo "===== obj $o ====="
  # show the object dict first
  DICT=$(pdf-parser -o "$o" "$F" 2>/dev/null | grep -v '^This program\|^Should you')
  echo "$DICT"
  # follow one level of references (JS action dicts point at separate stream objects)
  REFS="$REFS $(echo "$DICT" | grep '^ Referencing:' | grep -oE '[0-9]+ 0 R' | awk '{print $1}')"
  # dump decoded stream if present
  pdf-parser -o "$o" -f -d "$OUT/obj_$o.bin" "$F" >/dev/null 2>&1
  if [ -s "$OUT/obj_$o.bin" ]; then
    echo "--- decoded stream ($(stat -c%s "$OUT/obj_$o.bin") bytes), first 2000 bytes: ---"
    head -c 2000 "$OUT/obj_$o.bin"; echo
  fi
  echo
done

# second pass: dump referenced objects not already covered
for r in $(echo $REFS | tr ' ' '\n' | sort -un); do
  case " $OBJS " in *" $r "*) continue ;; esac
  echo "===== referenced obj $r ====="
  pdf-parser -o "$r" -f -d "$OUT/obj_$r.bin" "$F" >/dev/null 2>&1
  if [ -s "$OUT/obj_$r.bin" ]; then
    echo "--- decoded stream ($(stat -c%s "$OUT/obj_$r.bin") bytes), first 2000 bytes: ---"
    head -c 2000 "$OUT/obj_$r.bin"; echo
  else
    pdf-parser -o "$r" "$F" 2>/dev/null | grep -v '^This program\|^Should you'
  fi
  echo
done

echo "===== threat-pattern grep across all dumped streams ====="
grep -aioE 'https?://[^"< )]+|<script[^>]*>|app\.launchURL|util\.printf|unescape *\(|fromCharCode|eval *\(|\.exe\b|cmd\.exe|powershell|\\\\u[0-9a-f]{4}%?' \
  "$OUT"/*.bin 2>/dev/null | sort | uniq -c | sort -rn | head -50 \
  || echo "(no dumps or no matches)"
echo
echo "[i] dumps kept in $OUT for manual review; delete with: rm -rf $OUT"
exit 0
