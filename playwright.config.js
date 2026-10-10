const { defineConfig } = require("@playwright/test");

// UI проверяется на статическом сервере с подменённым API, без настоящего бота.
module.exports = defineConfig({
  testDir: "./tests/ui",
  timeout: 30000,
  use: {
    baseURL: "http://127.0.0.1:4173",
    // Обычно исключаем влияние кеша; отдельный тест PWA явно разрешает его.
    serviceWorkers: "block",
    // Стабилизируем снимки без изменения пользовательских настроек реальной панели.
    reducedMotion: "reduce",
    screenshot: "only-on-failure",
  },
  webServer: {
    command: "npx http-server cmd/hkc-web/static -p 4173 -a 127.0.0.1 -s",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: !process.env.CI,
    timeout: 30000,
  },
});
