// Host test: parse a minimal v1 snapshot with the vendored single-header
// ArduinoJson.  This file defines TEST_FRAMEWORK_MAIN so it owns the binary
// entry point; future test_*.cpp files just #include "framework.h".
#define TEST_FRAMEWORK_MAIN
#include "framework.h"

// Disable Arduino-specific extensions before pulling in the vendored header.
#define ARDUINOJSON_ENABLE_ARDUINO_STRING 0
#define ARDUINOJSON_ENABLE_ARDUINO_STREAM 0
#define ARDUINOJSON_ENABLE_STD_STREAM 0
#include <ArduinoJson.h>

TEST(smoke) {
    const char* json = "{\"v\":1}";
    JsonDocument doc;
    DeserializationError err = deserializeJson(doc, json);
    ASSERT_TRUE(err == DeserializationError::Ok);
    ASSERT_EQ(1, doc["v"].as<int>());
}
