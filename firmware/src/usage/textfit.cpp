// firmware/src/usage/textfit.cpp — measure-then-shrink text fitting.
//
// Pure C++17, no Arduino/M5 headers.
#include "usage/textfit.h"

#include <cstring>

namespace usage {

size_t fitRight(const char* in, char* out, size_t n,
                int maxPx, int (*measure)(const char*)) {
    if (n == 0) return 0;
    if (in == nullptr) {
        out[0] = '\0';
        return 0;
    }

    size_t inLen = std::strlen(in);

    // Try the full string first — if it fits, copy verbatim.
    if (measure(in) <= maxPx) {
        size_t i;
        for (i = 0; i < n - 1 && i < inLen; i++)
            out[i] = in[i];
        out[i] = '\0';
        return i;
    }

    // Shorten: try in[0..k) + ".." for decreasing k.
    // Buffer must hold k chars + ".." + NUL = k + 3 bytes.
    size_t maxK = (n > 3) ? n - 3 : 0;
    if (maxK > inLen) maxK = inLen;

    for (size_t k = maxK; k > 0; k--) {
        std::memcpy(out, in, k);
        out[k] = '.';
        out[k + 1] = '.';
        out[k + 2] = '\0';
        if (measure(out) <= maxPx) {
            return k + 2;
        }
    }

    // Try ".." alone (k == 0).
    if (n >= 3) {
        out[0] = '.';
        out[1] = '.';
        out[2] = '\0';
        if (measure(out) <= maxPx) {
            return 2;
        }
    }

    // Can't fit anything meaningful.
    out[0] = '\0';
    return 0;
}

} // namespace usage
