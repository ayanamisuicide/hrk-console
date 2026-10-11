const js = require("@eslint/js");
const globals = require("globals");

// Лёгкая проверка интерфейса: ошибки, которые ловятся без запуска (опечатки в именах, неиспользуемые
// переменные, недостижимый код). Стиль не навязывается.
module.exports = [
  { ignores: ["node_modules/**", "test-results/**", "playwright-report/**", "bin/**"] },
  js.configs.recommended,
  {
    files: ["cmd/hkc-web/static/**/*.js"],
    languageOptions: { ecmaVersion: "latest", sourceType: "module", globals: { ...globals.browser } },
    rules: { "no-unused-vars": ["error", { args: "none", caughtErrors: "none" }] },
  },
  {
    // Service worker и Playwright-тесты запускаются не как обычные модули браузера.
    files: ["cmd/hkc-web/static/sw.js"],
    languageOptions: { sourceType: "script", globals: { ...globals.serviceworker } },
  },
  {
    files: ["tests/**/*.js", "playwright.config.js", "eslint.config.js"],
    languageOptions: { ecmaVersion: "latest", sourceType: "commonjs", globals: { ...globals.node, ...globals.browser } },
    rules: { "no-unused-vars": ["error", { args: "none", caughtErrors: "none" }] },
  },
];
