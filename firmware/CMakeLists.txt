cmake_minimum_required(VERSION 3.16)
project(usage_tests CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

# Public headers live in src/ (future firmware core), third_party/ holds the
# vendored ArduinoJson single header, and test/host/ has the test framework
# plus individual *_test.cpp files.
include_directories(src third_party test/host)

# Production source: pure-C++17 module under src/usage/ (no Arduino/M5 headers).
file(GLOB_RECURSE USAGE_SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/src/usage/*.cpp)
# Host test sources.
file(GLOB TEST_SOURCES ${CMAKE_CURRENT_SOURCE_DIR}/test/host/*.cpp)

add_executable(usage_tests ${USAGE_SOURCES} ${TEST_SOURCES})

target_compile_options(usage_tests PRIVATE
    -Wall -Wextra -Werror
    -fsanitize=address,undefined
    -fno-omit-frame-pointer
)

target_link_options(usage_tests PRIVATE
    -fsanitize=address,undefined
)

enable_testing()
add_test(NAME usage_tests COMMAND usage_tests)
