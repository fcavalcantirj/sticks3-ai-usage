// firmware/src/usage/serial_proto.h — bounded serial-line formatters for the
// debug/protocol output.  Pure C++17, no Arduino/M5 headers.
#pragma once

#include <cstddef>
#include <cstdint>

namespace usage {

// fmtBoot formats the boot banner:
//   [BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0
int fmtBoot(char* out, size_t n, int board, uint32_t psram,
            const char* build, const char* fw);

// fmtNet formats a network state transition:
//   [NET] state=connected ip=192.168.0.77
//   [NET] state=connecting
//   [NET] state=lost
int fmtNet(char* out, size_t n, const char* state, const char* ip);

// fmtFetch formats a fetch result:
//   304 (no body):    [FETCH] code=304 rev=abcd1234 ms=120
//   200 (has body):   [FETCH] code=200 rev=abcd1234 seq=43 ms=310
//   <0  (error):      [FETCH] code=-1 err=timeout ms=8000
//  (rev is reused as the error description when code < 0.)
int fmtFetch(char* out, size_t n, int code, const char* rev,
             uint32_t seq, uint32_t ms);

// fmtRender formats a screen redraw:
//   [RENDER] page=1 lines=5 rev=abcd1234
int fmtRender(char* out, size_t n, uint8_t page, uint8_t lines,
              const char* rev);

// fmtErr formats an error line:
//   [ERR] <what>
int fmtErr(char* out, size_t n, const char* what);

// fmtHeap formats the 60 s heap watchdog line:
//   [HEAP] free=123456 min=65432
int fmtHeap(char* out, size_t n, uint32_t free, uint32_t min);

} // namespace usage
