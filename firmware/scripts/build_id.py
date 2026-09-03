# PlatformIO pre-script: injects the git identity of the working tree as
# USAGED_BUILD_ID so the firmware can report which commit produced it
# (boot EVENT, BUILD line, STATUS build= field, telemetry greeting).
#
# Notes:
#  - Changing the sha changes CPPDEFINES, which invalidates every object file
#    and forces a full rebuild. Accepted cost for this project size.
#  - Never prints anything but the short sha; no secrets are read.
#  - Falls back to "unknown" when git is unavailable (clean exports, CI).
import subprocess

Import("env")  # noqa: F821  (PlatformIO injects this)


def _git(args):
    try:
        return subprocess.check_output(
            ["git"] + args,
            cwd=env["PROJECT_DIR"],  # noqa: F821
            stderr=subprocess.DEVNULL,
        ).decode().strip()
    except Exception:
        return ""


sha = _git(["rev-parse", "--short", "HEAD"]) or "unknown"
if sha != "unknown" and _git(["status", "--porcelain"]):
    sha += "+dirty"

print("USAGED_BUILD_ID=%s" % sha)
env.Append(CPPDEFINES=[("USAGED_BUILD_ID", env.StringifyMacro(sha))])  # noqa: F821
