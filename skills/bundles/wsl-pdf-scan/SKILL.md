---
name: wsl-pdf-scan
description: Static malware triage of PDF files using WSL tools (pdfid, pdf-parser, python3). Produces hashes, active-content keyword scan, JavaScript/XFA/embedded-file inspection, URI extraction, text preview, and a risk verdict. Use when the user explicitly invokes /wsl-pdf-scan or asks to analyze whether a PDF is malicious using WSL.
---

# WSL PDF Malware Scan

Static (no-execution) triage of one or more PDF files via WSL. Never open the PDF in a viewer.

## Bundled scripts (use these first)

Let `SKILL=/mnt/c/<workspace>/.qoder/skills/wsl-pdf-scan/scripts`. All scripts accept Windows (`D:\temp\a.pdf`) or WSL paths — conversion via `wslpath` is built in.

| Script | Purpose |
|---|---|
| `scan.sh [--text] <file>...` | One-shot triage: fingerprint + pdfid + python cross-check (+ optional text dump). Multi-file. |
| `deep_dive.sh <file> --auto` | Auto-locate & dump all JS/XFA/EmbeddedFile/OpenAction objects, decode streams, threat-pattern grep. Or pass explicit object numbers. |
| `pdf_triage.py <file> [--text]` | Pure-stdlib scanner (works even without pdfid/pdf-parser). |

## Quoting rules (critical)

- File names often contain spaces or apostrophes (`Supplier's_...`). Inline `wsl.exe -e bash -c "..."` quoting WILL break on them (silent ExitCode 1).
- Preferred invocation — pass the file as a double-quoted **argument** to a bundled script:
  `wsl.exe -e bash "$SKILL/scan.sh" "D:\temp\Supplier's file.pdf"`
- Only if ad-hoc commands are unavoidable, write them into a temp `.sh` in the workspace and run it; delete temp scripts and `/tmp/pdfscan` when done.

## Workflow

### Step 1 — One-shot triage
```bash
wsl.exe -e bash "$SKILL/scan.sh" "<file1>" "<file2>"
```
Covers fingerprint (`file`/hashes), pdfid indicators, and the python cross-check in one pass.
Danger indicators: `/JS`, `/JavaScript`, `/OpenAction`, `/AA`, `/Launch`, `/EmbeddedFile`, `/RichMedia`, `/XFA`, `/JBIG2Decode`, `/Encrypt`. If **all zero** → skip to Step 3.

### Step 2 — Deep-dive flagged objects
```bash
wsl.exe -e bash "$SKILL/deep_dive.sh" "<file>" --auto
```
Dumps each flagged object's dict + decoded stream to `/tmp/pdfscan/`, then greps all dumps for threat patterns (URLs, launchURL, unescape/fromCharCode/eval, .exe/powershell).
Interpretation:
- Known-benign: Adobe LiveCycle `!ADBE::*VersChk*` version-check JS boilerplate (alerts + `cgi.adobe.com` URL only).
- Standard namespace URLs (xfa.org, ns.adobe.com, w3.org, purl.org) are benign. LiveCycle forms legitimately produce ~10 `/EmbeddedFile` XFA packets — not smuggled files.
- `/OpenAction`, `/AA`, `/Launch`, `/SubmitForm`, `/GoToR` pointing at scripts/executables/external hosts = malicious until proven otherwise.
For manual inspection of a specific object: `pdf-parser -o <N> -f -d /tmp/out.bin "$F"`.

### Step 3 — Content & context check
- Metadata (Producer/Creator/dates/Author) is already in `scan.sh` output.
- Re-run with `--text` to extract visible text and confirm it matches the filename's claimed topic:
  `wsl.exe -e bash "$SKILL/scan.sh" --text "<file>"`
- Validate every `/URI`: domain must match the purported sender organization; flag lookalike domains, IP literals, URL shorteners, non-HTTPS credential pages.
- Even for a technically clean file, warn about social-engineering context (RfQ/invoice/shipping lures are common BEC bait) if the document solicits replies, payments, or credentials.
- Clean up: `wsl.exe -e bash -c "rm -rf /tmp/pdfscan"` after reporting.

## Verdict report format

Per file, output a Markdown section with:
1. Table: size/version/pages, MD5, SHA256, producer/creator/dates.
2. Active-content findings with per-indicator explanation (including why flagged items are benign, if so).
3. URI list with legitimacy assessment.
4. Content summary vs. filename claim.
5. Verdict: **恶意 / 可疑 / 低风险（未发现恶意迹象）** + recommendation (VirusTotal hash lookup, sandboxed viewer, sender verification).

Match the user's language (default Chinese if the user writes Chinese).
