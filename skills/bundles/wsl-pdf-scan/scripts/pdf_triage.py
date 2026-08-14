#!/usr/bin/env python3
"""Static PDF malware triage - stdlib only (zlib).

Usage:
    python3 pdf_triage.py <file.pdf>          # full triage
    python3 pdf_triage.py <file.pdf> --text   # also dump extracted page text
"""
import re
import sys
import zlib

path = sys.argv[1]
want_text = '--text' in sys.argv[2:]
data = open(path, 'rb').read()

print(f"[i] size={len(data)} bytes, header={data[:16]!r}")
if not data.startswith(b'%PDF-'):
    print("[!] NOT a PDF header - possible disguised file type")

# 1. Decompress every FlateDecode stream and rescan keywords inside
stream_re = re.compile(rb'stream\r?\n(.*?)\r?\nendstream', re.S)
streams = stream_re.findall(data)
print(f"[i] stream objects found: {len(streams)}")

decompressed = b''
fail = 0
for s in streams:
    try:
        decompressed += zlib.decompress(s)
    except Exception:
        fail += 1
print(f"[i] streams decompressed OK: {len(streams) - fail}, failed/non-flate: {fail}")

keywords = [b'/JavaScript', b'/JS', b'/OpenAction', b'/AA', b'/Launch',
            b'/EmbeddedFile', b'/RichMedia', b'/XFA', b'/SubmitForm',
            b'/GoToR', b'/GoToE', b'/URI', b'cmd.exe', b'powershell',
            b'unescape(', b'String.fromCharCode', b'.exe']
print("\n[keyword scan: raw file + decompressed streams]")
for kw in keywords:
    n_raw = data.count(kw)
    n_dec = decompressed.count(kw)
    if n_raw or n_dec:
        print(f"  {kw.decode(errors='replace')}: raw={n_raw} decompressed={n_dec}")

# 2. Extract all URIs (raw + decompressed, literal + hex-encoded)
uri_re = re.compile(rb'/URI\s*\(([^)]*)\)')
uris = set(uri_re.findall(data)) | set(uri_re.findall(decompressed))
print(f"\n[URIs found: {len(uris)}]")
for u in sorted(uris):
    print("  ", u.decode(errors='replace'))
urihex_re = re.compile(rb'/URI\s*<([0-9A-Fa-f\s]+)>')
for m in set(urihex_re.findall(data)) | set(urihex_re.findall(decompressed)):
    print("   (hex)", bytes.fromhex(m.decode().replace(' ', '')).decode(errors='replace'))

# 3. Object inventory
obj_re = re.compile(rb'(\d+)\s+(\d+)\s+obj(.*?)endobj', re.S)
types = {}
for num, gen, body in obj_re.findall(data):
    m = re.search(rb'/Type\s*/(\w+)', body)
    t = m.group(1).decode() if m else '(none)'
    types[t] = types.get(t, 0) + 1
print(f"\n[object types] {types}")

# 4. Suspicious dict keys hidden inside ObjStm/decompressed content
susp_re = re.compile(rb'/(OpenAction|AA|Launch|EmbeddedFiles?|Filespec|JavaScript|JS)\b')
hits = sorted(set(h.decode() for h in susp_re.findall(decompressed)))
print(f"[suspicious dict keys in ObjStm/decompressed]: {hits or 'none'}")

# 5. Metadata
print("\n[metadata]")
for kw in (b'/Producer', b'/Creator', b'/CreationDate', b'/ModDate', b'/Author', b'/Title'):
    for src in (data, decompressed):
        m = re.search(re.escape(kw) + rb'\s*\(([^)]{0,120})\)', src)
        if m:
            print(f"  {kw.decode()}: {m.group(1).decode(errors='replace')}")
            break

# 6. Data after last %%EOF (smuggling trick)
eof = data.rfind(b'%%EOF')
trailing = len(data) - (eof + 5) if eof >= 0 else -1
print(f"\n[trailing bytes after last %%EOF]: {trailing}")

# 7. Embedded executable signatures
for sig, name in [(b'MZ\x90', 'PE exe'), (b'PK\x03\x04', 'ZIP/OOXML'),
                  (b'\x7fELF', 'ELF'), (b'#!/', 'script shebang')]:
    for label, src in (('raw', data), ('decompressed', decompressed)):
        idx = src.find(sig)
        if idx > 1024:  # ignore header area
            print(f"[!] {name} signature in {label} at offset {idx}")

# 8. Optional page text extraction (Tj / TJ operators)
if want_text:
    parts = re.findall(rb'\((.*?)(?<!\\)\)\s*Tj', decompressed)
    parts += [m for arr in re.findall(rb'\[(.*?)\]\s*TJ', decompressed, re.S)
              for m in re.findall(rb'\((.*?)(?<!\\)\)', arr)]
    print("\n[extracted text, first 2500 chars]")
    print(b' '.join(parts).decode('latin-1', errors='replace')[:2500])
