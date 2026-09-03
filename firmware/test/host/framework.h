// firmware/test/host/framework.h — minimal header-only host test harness.
//
// Usage: in exactly one .cpp per test binary, define TEST_FRAMEWORK_MAIN
// before including this header to emit main().  In all files just include it
// and use the TEST/ASSERT_* macros.
#pragma once

#include <cstdio>
#include <stdexcept>
#include <string>
#include <vector>

// --- registry ---------------------------------------------------------------

struct TestCase {
    const char* name;
    void (*fn)();
};

// inline (C++17) so the function-local static is shared across TUs.
inline std::vector<TestCase>& testRegistry() {
    static std::vector<TestCase> r;
    return r;
}

// --- registration macro -----------------------------------------------------

#define TEST(name)                                                            \
    static void test_##name();                                               \
    static bool _registered_##name =                                          \
        (testRegistry().push_back({#name, test_##name}), true);             \
    static void test_##name()

// --- assertion macros -------------------------------------------------------
// On failure: print to stderr and throw, aborting only this test.

#define ASSERT_TRUE(cond)                                                      \
    do {                                                                       \
        if (!(cond)) {                                                         \
            std::fprintf(stderr, "ASSERT_TRUE(%s) failed at %s:%d\n",        \
                         #cond, __FILE__, __LINE__);                           \
            throw std::runtime_error(#cond);                                   \
        }                                                                      \
    } while (0)

#define ASSERT_EQ(a, b)                                                        \
    do {                                                                       \
        auto _va = (a);                                                        \
        auto _vb = (b);                                                        \
        if (!(_va == _vb)) {                                                   \
            std::fprintf(stderr, "ASSERT_EQ(%s,%s) failed at %s:%d\n",         \
                         #a, #b, __FILE__, __LINE__);                          \
            throw std::runtime_error(#a);                                      \
        }                                                                      \
    } while (0)

#define ASSERT_STREQ(a, b)                                                     \
    do {                                                                       \
        if (std::string(a) != std::string(b)) {                                \
            std::fprintf(stderr, "ASSERT_STREQ(%s,%s) failed at %s:%d\n",      \
                         #a, #b, __FILE__, __LINE__);                          \
            throw std::runtime_error(#a);                                      \
        }                                                                      \
    } while (0)

// --- entry point ------------------------------------------------------------
// Only compiled when the test binary's main .cpp defines TEST_FRAMEWORK_MAIN.

#ifdef TEST_FRAMEWORK_MAIN
int main() {
    int failed = 0;
    for (const auto& tc : testRegistry()) {
        try {
            tc.fn();
            std::printf("PASS %s\n", tc.name);
        } catch (const std::exception& e) {
            std::printf("FAIL %s\n", tc.name);
            ++failed;
        }
    }
    std::printf("Failed %d\n", failed);
    return failed > 0 ? 1 : 0;
}
#endif
