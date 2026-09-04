// firmware/src/usage/textfit.h — measure-then-shrink text fitting.
//
// Pure C++17, no Arduino/M5 headers.  Given a measure callback that returns
// the pixel width of a string, fitRight tries to fit `in` into at most
// `maxPx` pixels.  If it fits unchanged it is copied verbatim; otherwise
// trailing characters are dropped and ".." is appended.
#pragma once

#include <cstddef>

namespace usage {

// fitRight fits `in` into at most `maxPx` pixels (as measured by `measure`).
// The result is written to `out` (NUL-terminated within `n` bytes) and the
// fitted length (excluding NUL) is returned.
size_t fitRight(const char* in, char* out, size_t n,
                int maxPx, int (*measure)(const char*));

} // namespace usage
