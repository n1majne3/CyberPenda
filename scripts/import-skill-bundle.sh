#!/usr/bin/env bash
# Import a local Skill bundle directory into the CyberPenda daemon library.
# Usage: scripts/import-skill-bundle.sh <bundle-dir> [daemon-base-url]
# The daemon default is http://127.0.0.1:8787 (make dev).
set -euo pipefail

bundle_dir="${1:?usage: import-skill-bundle.sh <bundle-dir> [daemon-base-url]}"
base_url="${2:-${CYBERPENDA_DAEMON_URL:-http://127.0.0.1:8787}}"

if [ ! -f "$bundle_dir/SKILL.md" ]; then
  echo "error: $bundle_dir/SKILL.md not found" >&2
  exit 2
fi

python3 - "$bundle_dir" "$base_url" <<'PY'
import json
import pathlib
import re
import sys
import urllib.request

bundle = pathlib.Path(sys.argv[1])
base = sys.argv[2].rstrip("/")

text = (bundle / "SKILL.md").read_text(encoding="utf-8")
front = text.split("---", 2)
if len(front) < 3:
    sys.exit("error: SKILL.md front-matter is missing")
name = description = ""
for line in front[1].splitlines():
    match = re.match(r"^(name|description):\s*(.+)$", line.strip())
    if match:
        key, value = match.groups()
        if key == "name":
            name = value.strip().strip('"')
        else:
            description = value.strip().strip('"')
if not name:
    sys.exit("error: SKILL.md front-matter has no name")

files = {}
for path in sorted(bundle.rglob("*")):
    if path.is_file():
        files[str(path.relative_to(bundle))] = path.read_text(encoding="utf-8")

payload = {
    "name": name,
    "description": description,
    "source_provenance": {"kind": "local"},
    "files": files,
}
request = urllib.request.Request(
    f"{base}/api/skills/{name}",
    data=json.dumps(payload).encode("utf-8"),
    method="PUT",
    headers={"Content-Type": "application/json"},
)
with urllib.request.urlopen(request) as response:
    body = response.read().decode("utf-8")
print(f"imported skill '{name}' ({len(files)} files) -> {base}")
print(body)
PY
