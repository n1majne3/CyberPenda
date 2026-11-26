# Real Pi Resume replay records

`pi-resume-retained.jsonl` contains six unchanged records from the real local
Qwen/Pi/Sandbox Session captured on 2026-11-26. It is not a synthetic transcript.
The source is `pi-native-raw-0.jsonl` in the frozen Resume verification bundle
linked from [the baseline recording](https://app.devin.ai/sessions/aafe5df495d349c0aa7a49ac83be966f?testRecording=9f645c8c-3dc1-4c0b-b421-b384f49c04e6).

The selected records are the Session header, three retained assistant messages
(`fa1ca303`, `21788a3d`, `5272d6ce`), and the Resume user/assistant pair
(`8ad02701`, `af6d826a`). System instructions, tools, and unrelated records are
excluded, not rewritten. The test injects a duplicate final record and a missing
durable record to check replay deduplication and crash recovery separately.

The captured model did not follow the requested Resume answer. These records
prove output identity and retention, not successful model instruction following.
