// firmware/src/usage/serial_proto.cpp — bounded serial-line formatters.
//
// Pure C++17, no Arduino/M5 headers.  Every function writes into the
// caller-provided buffer (size n) and returns the number of characters that
// *would* have been written (excluding the NUL terminator), exactly like
// snprintf — so a caller can detect truncation by comparing the return value
// against n.
#include "usage/serial_proto.h"

#include <cstdio>
#include <cstring>

namespace usage {

// fmtBoot formats the boot banner:
//   [BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0
int fmtBoot(char* out, size_t n, int board, uint32_t psram,
            const char* build, const char* fw) {
    return std::snprintf(out, n, "[BOOT] board=%d psram=%u build=%s fw=%s",
                         board, static_cast<unsigned int>(psram),
                         build != nullptr ? build : "",
                         fw != nullptr ? fw : "");
}

// fmtNet formats a network state transition:
//   [NET] state=connected ip=192.168.0.77
//   [NET] state=connecting
//   [NET] state=lost
// The ip field is omitted when ip is null or empty.
int fmtNet(char* out, size_t n, const char* state, const char* ip) {
    const char* s = state != nullptr ? state : "";
    if (ip != nullptr && ip[0] != '\0') {
        return std::snprintf(out, n, "[NET] state=%s ip=%s", s, ip);
    }
    return std::snprintf(out, n, "[NET] state=%s", s);
}

// fmtFetch formats a fetch result:
//   304 (no body):    [FETCH] code=304 rev=abcd1234 ms=120
//   200 (has body):   [FETCH] code=200 rev=abcd1234 seq=43 ms=310
//   <0  (error):      [FETCH] code=-1 err=timeout ms=8000
//  (rev is reused as the error description when code < 0.)
int fmtFetch(char* out, size_t n, int code, const char* rev,
             uint32_t seq, uint32_t ms) {
    const char* desc = rev != nullptr ? rev : "";
    if (code < 0) {
        return std::snprintf(out, n, "[FETCH] code=%d err=%s ms=%u",
                             code, desc, static_cast<unsigned int>(ms));
    }
    if (code == 304) {
        return std::snprintf(out, n, "[FETCH] code=%d rev=%s ms=%u",
                             code, desc, static_cast<unsigned int>(ms));
    }
    return std::snprintf(out, n, "[FETCH] code=%d rev=%s seq=%u ms=%u",
                         code, desc, static_cast<unsigned int>(seq),
                         static_cast<unsigned int>(ms));
}

// fmtRender formats a screen redraw:
//   [RENDER] page=1 lines=5 rev=abcd1234
int fmtRender(char* out, size_t n, uint8_t page, uint8_t lines,
              const char* rev) {
    return std::snprintf(out, n, "[RENDER] page=%u lines=%u rev=%s",
                         static_cast<unsigned int>(page),
                         static_cast<unsigned int>(lines),
                         rev != nullptr ? rev : "");
}

// fmtErr formats an error line:
//   [ERR] <what>
int fmtErr(char* out, size_t n, const char* what) {
    return std::snprintf(out, n, "[ERR] %s",
                         what != nullptr ? what : "");
}

// fmtHeap formats the 60 s heap watchdog line:
//   [HEAP] free=123456 min=65432
int fmtHeap(char* out, size_t n, uint32_t free, uint32_t min) {
    return std::snprintf(out, n, "[HEAP] free=%u min=%u",
                         static_cast<unsigned int>(free),
                         static_cast<unsigned int>(min));
}

// fmtWake formats a deep-sleep wake event:
//   [WAKE] cause=ext0 vbus=4904
int fmtWake(char* out, size_t n, const char* cause, uint32_t vbusMv) {
    return std::snprintf(out, n, "[WAKE] cause=%s vbus=%u",
                         cause != nullptr ? cause : "unknown",
                         static_cast<unsigned int>(vbusMv));
}

// fmtSleep formats a sleep entry event:
//   [SLEEP] reason=battery
int fmtSleep(char* out, size_t n, const char* reason) {
    return std::snprintf(out, n, "[SLEEP] reason=%s",
                         reason != nullptr ? reason : "");
}

// fmtOta formats an OTA event line.  kind selects the line shape:
//   "start" ->  [OTA] start
//   "pct"   ->  [OTA] pct=<param>     (param = 0..100)
//   "end"   ->  [OTA] end
//   "err"   ->  [OTA] err=<param>     (param = error code)
int fmtOta(char* out, size_t n, const char* kind, unsigned int param) {
    if (kind != nullptr && std::strcmp(kind, "pct") == 0) {
        return std::snprintf(out, n, "[OTA] pct=%u",
                             static_cast<unsigned int>(param));
    }
    if (kind != nullptr && std::strcmp(kind, "err") == 0) {
        return std::snprintf(out, n, "[OTA] err=%u",
                             static_cast<unsigned int>(param));
    }
    if (kind != nullptr && std::strcmp(kind, "start") == 0) {
        return std::snprintf(out, n, "[OTA] start");
    }
    if (kind != nullptr && std::strcmp(kind, "end") == 0) {
        return std::snprintf(out, n, "[OTA] end");
    }
    // Unknown kind: fall back to an error line.
    return std::snprintf(out, n, "[OTA] err=0");
}

// fmtGesture formats a gesture event line:
//   [GESTURE] flip rot=3
int fmtGesture(char* out, size_t n, uint8_t rot) {
    return std::snprintf(out, n, "[GESTURE] flip rot=%u",
                         static_cast<unsigned int>(rot));
}

// fmtBtn formats a button event line:
//   [BTN] a_hold refresh
//   [BTN] a_click page
int fmtBtn(char* out, size_t n, const char* event) {
    return std::snprintf(out, n, "[BTN] %s",
                         event != nullptr ? event : "");
}

// fmtBatt formats a battery change line:
//   [BATT] pct=87 usb=1
//   [BATT] pct=27 usb=0
int fmtBatt(char* out, size_t n, int pct, int usb) {
    return std::snprintf(out, n, "[BATT] pct=%d usb=%d",
                         pct, usb);
}

} // namespace usage
