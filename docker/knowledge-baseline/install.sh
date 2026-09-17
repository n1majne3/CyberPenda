#!/usr/bin/env bash
# Build the offline knowledge baseline under /opt/knowledge. Shared by the
# TSecBench Hosted Image and the Sandbox image so challenge work sees an
# identical reference layout in both. The baseline is read-only reference
# material: HackTricks methodology, PayloadsAllTheThings technique and
# payload references, and a curated sparse SecLists wordlist subset.
# Upstream content is pinned by full commit SHA; blob-less fetches keep the
# layer small. The full seclists package and full-size dictionaries stay
# excluded for delivery size.
set -euo pipefail

HACKTRICKS_SHA="${HACKTRICKS_SHA:-9085b5e1c9ca764b88b6f5f233df40a0890577d8}"
PATT_SHA="${PATT_SHA:-3ac27901c711bdf3f5b65a7b1d1820a1f65bd09a}"
SECLISTS_SHA="${SECLISTS_SHA:-8f4c1846cdb02a7024bd09decce1afc1ef5df46b}"

test -n "$HACKTRICKS_SHA"
test -n "$PATT_SHA"
test -n "$SECLISTS_SHA"

mkdir -p /opt/knowledge/hacktricks /opt/knowledge/payloads-all-the-things /opt/knowledge/wordlists

git -C /opt/knowledge/hacktricks init -q
git -C /opt/knowledge/hacktricks remote add origin https://github.com/carlospolop/hacktricks.git
git -C /opt/knowledge/hacktricks sparse-checkout set --no-cone \
  '/src/**' '!/src/images/**' '!/src/banners/**' '!/src/files/**'
git -C /opt/knowledge/hacktricks fetch -q --depth 1 --filter=blob:none origin "$HACKTRICKS_SHA"
git -C /opt/knowledge/hacktricks checkout -q --detach FETCH_HEAD

git -C /opt/knowledge/payloads-all-the-things init -q
git -C /opt/knowledge/payloads-all-the-things remote add origin https://github.com/swisskyrepo/PayloadsAllTheThings.git
git -C /opt/knowledge/payloads-all-the-things fetch -q --depth 1 --filter=blob:none origin "$PATT_SHA"
git -C /opt/knowledge/payloads-all-the-things checkout -q --detach FETCH_HEAD

git -C /opt/knowledge/wordlists init -q
git -C /opt/knowledge/wordlists remote add origin https://github.com/danielmiessler/SecLists.git
git -C /opt/knowledge/wordlists sparse-checkout set --no-cone \
  '/Discovery/Web-Content/common.txt' \
  '/Discovery/Web-Content/raft-small-directories.txt' \
  '/Discovery/Web-Content/raft-medium-directories.txt' \
  '/Passwords/Common-Credentials/10k-most-common.txt' \
  '/Passwords/Common-Credentials/100k-most-used-passwords-NCSC.txt' \
  '/Usernames/top-usernames-shortlist.txt' \
  '/Usernames/cirt-default-usernames.txt'
git -C /opt/knowledge/wordlists fetch -q --depth 1 --filter=blob:none origin "$SECLISTS_SHA"
git -C /opt/knowledge/wordlists checkout -q --detach FETCH_HEAD

mkdir -p /opt/knowledge/wordlists/web /opt/knowledge/wordlists/passwords /opt/knowledge/wordlists/usernames
mv /opt/knowledge/wordlists/Discovery/Web-Content/common.txt \
   /opt/knowledge/wordlists/Discovery/Web-Content/raft-small-directories.txt \
   /opt/knowledge/wordlists/Discovery/Web-Content/raft-medium-directories.txt \
   /opt/knowledge/wordlists/web/
mv /opt/knowledge/wordlists/Passwords/Common-Credentials/10k-most-common.txt \
   /opt/knowledge/wordlists/Passwords/Common-Credentials/100k-most-used-passwords-NCSC.txt \
   /opt/knowledge/wordlists/passwords/
mv /opt/knowledge/wordlists/Usernames/top-usernames-shortlist.txt \
   /opt/knowledge/wordlists/Usernames/cirt-default-usernames.txt \
   /opt/knowledge/wordlists/usernames/

rm -rf /opt/knowledge/hacktricks/.git \
       /opt/knowledge/payloads-all-the-things/.git \
       /opt/knowledge/wordlists/.git \
       /opt/knowledge/wordlists/Discovery \
       /opt/knowledge/wordlists/Passwords \
       /opt/knowledge/wordlists/Usernames

test -s /opt/knowledge/hacktricks/src/SUMMARY.md
test -d /opt/knowledge/hacktricks/src/pentesting-web
test -d /opt/knowledge/hacktricks/src/binary-exploitation
test -s "/opt/knowledge/payloads-all-the-things/SQL Injection/README.md"
test -d "/opt/knowledge/payloads-all-the-things/Server Side Request Forgery"
test -s /opt/knowledge/wordlists/web/common.txt
test -s /opt/knowledge/wordlists/web/raft-small-directories.txt
test -s /opt/knowledge/wordlists/web/raft-medium-directories.txt
test -s /opt/knowledge/wordlists/passwords/10k-most-common.txt
test -s /opt/knowledge/wordlists/passwords/100k-most-used-passwords-NCSC.txt
test -s /opt/knowledge/wordlists/usernames/top-usernames-shortlist.txt
test -s /opt/knowledge/wordlists/usernames/cirt-default-usernames.txt

chmod -R a+r /opt/knowledge
