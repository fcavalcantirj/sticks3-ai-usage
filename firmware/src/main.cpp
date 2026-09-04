// firmware/src/main.cpp — Arduino entry points for the StickS3 usage monitor.
//
// Includes <M5Unified.h> (allowed for main.cpp) and delegates all M5 calls to
// the HAL layer; usage/ formatters stay pure C++17.
#include <M5Unified.h>

#include "hal/sticks3/board.h"
#include "hal/sticks3/net.h"
#include "hal/sticks3/screen.h"
#include "usage/serial_proto.h"

using namespace sticks3;

void setup() {
    boardInit();
    Serial.begin(115200);

    char buf[64];
    usage::fmtBoot(buf, sizeof(buf), boardId(), psramBytes(), buildId());
    serialLine(buf);

    drawBootScreen(buildId());
    netBegin();
}

void loop() {
    M5.update();
    uint32_t now = nowMs();
    netUpdate(now);
    delay(1);
}
