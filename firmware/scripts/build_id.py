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

# The firmware version is the RELEASE TAG, not a number maintained by hand.
# It used to be a literal in main.cpp that drifted: the stick reported
# fw=1.0.0 while the project shipped v0.1.9. Deriving it from the same git
# tags the daemon and the release tarball use means there is one source of
# truth and nothing to remember to bump.
tag = _git(["describe", "--tags", "--abbrev=0"]) or ""
version = tag[1:] if tag.startswith("v") else (tag or "0.0.0-dev")

print("USAGED_BUILD_ID=%s" % sha)
print("USAGED_FW_VERSION=%s" % version)
env.Append(CPPDEFINES=[  # noqa: F821
    ("USAGED_BUILD_ID", env.StringifyMacro(sha)),  # noqa: F821
    ("USAGED_FW_VERSION", env.StringifyMacro(version)),  # noqa: F821
])
