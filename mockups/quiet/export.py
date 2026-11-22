from pathlib import Path
import re
import sys

root = Path(__file__).resolve().parent
html = (root / "index.html").read_text(encoding="utf-8")
html = re.sub(r'<link rel="stylesheet" href="([^"]+)">', lambda match: "<style>\n" + (root / match[1]).read_text(encoding="utf-8") + "</style>", html)
html = re.sub(r'<script defer src="([^"]+)"></script>', lambda match: "<script>\n" + (root / match[1]).read_text(encoding="utf-8") + "</script>", html)
sys.stdout.write(html)
