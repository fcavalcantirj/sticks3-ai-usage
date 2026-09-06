#!/bin/sh
# device-report.sh — what the StickS3 actually did, from the access log.
#
# Prints one row per device request: status, the age the DEVICE claimed, the
# age the SERVER knew, their drift, and the wall gap since the previous row.
# Sleep shows up as a large wall gap; a correct freshness pipeline shows drift
# near zero across it.
#
# Two false "offset" defects were filed on 2026-09-06 by reconstructing the
# server baseline by hand. This reads the baseline the server itself logged.
#
# Usage: sh scripts/device-report.sh [rows] [log-path]
set -eu
rows=${1:-25}
log=${2:-$HOME/Library/Logs/usaged/usaged.err.log}
[ -f "$log" ] || { echo "no log at $log" >&2; exit 1; }
ROWS="$rows" LOG="$log" python3 - <<'PY'
import json, os, sys
rows = int(os.environ["ROWS"]); log = os.environ["LOG"]
recs = []
for line in open(log, errors="replace"):
    if '"msg":"access"' not in line:
        continue
    try:
        d = json.loads(line)
    except ValueError:
        continue
    ua = d.get("user_agent") or ""
    if not ua.startswith("sticks3-usage/"):
        continue          # devices only; browsers and curl are noise here
    recs.append(d)
if not recs:
    print("no device requests logged yet"); sys.exit(0)
recs = recs[-rows:]
print(f"{'time':8}  {'code':4}  {'device':>8}  {'server':>8}  {'drift':>7}  {'gap':>7}  build")
prev = None
worst = 0
for d in recs:
    t = d["time"][11:19]
    h, m, s = (int(x) for x in t.split(":"))
    now = h*3600 + m*60 + s
    gap = f"+{now-prev}s" if prev is not None else ""
    prev = now
    dev = d.get("age_s", "-")
    srv = d.get("server_age_s", "-")
    dr  = d.get("drift_s")
    if isinstance(dr, (int, float)):
        worst = max(worst, abs(dr))
        drs = f"{dr:+d}s"
    else:
        drs = "-"
    build = (d.get("user_agent") or "").split("/", 1)[-1]
    print(f"{t}  {d.get('status'):>4}  {str(dev):>8}  {str(srv):>8}  {drs:>7}  {gap:>7}  {build}")
print()
compared = sum(1 for d in recs if isinstance(d.get("drift_s"), (int, float)))
if compared == 0:
    print("NO COMPARISON AVAILABLE — none of these rows carry server_age_s.")
    print("  Either the running agent predates the drift logging, or the device")
    print("  is not sending age_s. Rebuild and kickstart, then re-run.")
    print("  Silence is NOT a pass: an empty comparison says nothing about freshness.")
elif worst <= 5:
    print(f"freshness OK — worst drift {worst}s across {compared} compared rows")
else:
    print(f"DRIFT {worst}s — the device's idea of data age disagrees with the server")
    print("  a large drift right after a flash is expected on the FIRST request only:")
    print("  that value is the device's pre-fetch estimate, before the response")
    print("  teaches it the server's real age. Judge from the rows after it.")
PY
