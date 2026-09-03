#include <M5Unified.h>

void setup() {
  auto cfg = M5.config();
  cfg.internal_imu = false;
  cfg.internal_spk = false;
  cfg.internal_mic = false;
  cfg.output_power = false;
  M5.begin(cfg);

  Serial.begin(115200);

  M5.Display.setRotation(1);
  M5.Display.setBrightness(80);

  const char *msg = "AI USAGE";
  M5.Display.setTextSize(2);
  M5.Display.setTextColor(0xFFFF);
  int16_t tw = M5.Display.textWidth(msg);
  int16_t th = M5.Display.fontHeight();
  M5.Display.setCursor((M5.Display.width() - tw) / 2, (M5.Display.height() - th) / 2);
  M5.Display.println(msg);

  Serial.printf("[BOOT] board=%d psram=%d build=%s fw=0.1.0\n",
                (int)M5.getBoard(), (int)ESP.getPsramSize(), USAGED_BUILD_ID);
}

void loop() {
  M5.update();
  delay(10);
}
