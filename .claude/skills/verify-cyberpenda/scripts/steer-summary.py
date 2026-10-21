#!/usr/bin/env python3
"""Summarize Steer verification evidence without contacting the app."""

import json
import sys
from pathlib import Path

root = Path(sys.argv[1]).resolve() / "evidence"
for path in sorted(root.glob("*.json")):
    data = json.loads(path.read_text(encoding="utf-8"))
    if "owner" not in data:
        continue
    owner = data["owner"]
    controls = owner.get("runtime_controls", {})
    continuation = owner.get("latest_continuation") or {}
    assistant = [e for e in data["transcript"]["entries"]
                 if e.get("role") == "assistant" and e.get("kind") == "message"]
    settlements = [item for item in data["timeline"]["items"]
                   if "settle" in str(item.get("content", "")).lower() or "steer" in str(item.get("content", "")).lower()]
    print(f"{path.stem}: {owner.get('runtime_activity')} steer={controls.get('native_steer_mode')}/{controls.get('native_steer_state')} "
          f"req={controls.get('native_steer_request_id', '')[:8]} cont=#{continuation.get('number')} "
          f"cid={str(continuation.get('container_id'))[:12]} native={str(continuation.get('native_session_id'))[-8:]} "
          f"assistant={len(assistant)} last={assistant[-1]['text'][:40]!r}")
    for item in settlements[-4:]:
        print(f"    {item['type']} {item.get('content', '')[:200]!r}")
