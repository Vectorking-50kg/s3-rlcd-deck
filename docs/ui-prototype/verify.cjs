/* PROTOTYPE VERIFIER — run with the bundled Playwright runtime. */
const { chromium } = require("playwright");
const path = require("node:path");
const fs = require("node:fs");

const baseURL = process.argv[2] || "http://127.0.0.1:4173/";
const routes = [
  "overview", "providers", "provider-editor", "history", "sessions",
  "serial", "serial-presets", "devices", "network", "setup", "deck",
  "system", "updates", "backup", "diagnostics", "tray", "login",
];
const exceptionalStates = ["loading", "empty", "error"];
const failures = [];
const systemBrowsers = [
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
];
const executablePath = systemBrowsers.find((candidate) => fs.existsSync(candidate));

function captureErrors(page, scope) {
  page.on("pageerror", (error) => failures.push(`${scope}: pageerror: ${error.message}`));
  page.on("console", (message) => {
    if (message.type() === "error") failures.push(`${scope}: console: ${message.text()}`);
  });
}

async function auditRoute(page, route, scope) {
  await page.goto(`${baseURL}#${route}`, { waitUntil: "networkidle" });
  await page.waitForSelector("#prototype-main");
  const audit = await page.evaluate(() => ({
    h1: document.querySelector("h1")?.textContent?.trim(),
    width: document.documentElement.scrollWidth,
    viewport: document.documentElement.clientWidth,
    root: document.querySelector(".prototype-root")?.className,
    dockItems: document.querySelectorAll(".c-dock .dock-button").length,
  }));
  if (!audit.h1) failures.push(`${scope}/${route}: missing H1`);
  if (audit.width > audit.viewport + 1) failures.push(`${scope}/${route}: horizontal overflow ${audit.width} > ${audit.viewport}`);
  if (!audit.root?.includes("variant-c")) failures.push(`${scope}/${route}: Scheme C root missing`);
  if (audit.dockItems !== 6) failures.push(`${scope}/${route}: expected 5 modules + access dock item, got ${audit.dockItems}`);
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(executablePath ? { executablePath } : {}) });
  const output = path.join(__dirname, "screenshots");
  fs.mkdirSync(output, { recursive: true });

  for (const viewport of [{ name: "desktop", width: 1440, height: 1000 }, { name: "mobile", width: 375, height: 812 }]) {
    const context = await browser.newContext({ viewport: { width: viewport.width, height: viewport.height }, reducedMotion: "reduce", isMobile: viewport.name === "mobile" });
    const page = await context.newPage();
    captureErrors(page, viewport.name);
    for (const route of routes) await auditRoute(page, route, viewport.name);
    await context.close();
  }

  const statesContext = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
  const statesPage = await statesContext.newPage();
  captureErrors(statesPage, "states");
  for (const route of routes.filter((route) => route !== "login")) {
    for (const viewState of exceptionalStates) {
      await statesPage.goto(`${baseURL}#${route}`, { waitUntil: "networkidle" });
      await statesPage.getByLabel("切换页面评审状态").selectOption(viewState);
      const heading = await statesPage.locator("h1").textContent();
      if (!heading?.trim()) failures.push(`state/${route}/${viewState}: missing H1`);
      const width = await statesPage.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }));
      if (width.scroll > width.client + 1) failures.push(`state/${route}/${viewState}: horizontal overflow`);
    }
  }
  await statesContext.close();

  const interactionContext = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
  const page = await interactionContext.newPage();
  captureErrors(page, "interaction");

  await page.goto(`${baseURL}#providers`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "编辑", exact: true }).first().click();
  if (!(await page.getByRole("heading", { name: "编辑 OpenRouter" }).isVisible())) failures.push("interaction: Provider editor did not open");
  await page.getByRole("tab", { name: "数据映射" }).click();
  if (!(await page.getByRole("heading", { name: "规范化字段" }).isVisible())) failures.push("interaction: Provider mapping tab did not render");

  await page.goto(`${baseURL}#history`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "清空历史…" }).click();
  if (!(await page.getByRole("dialog").isVisible())) failures.push("interaction: clear history confirmation did not open");
  await page.getByRole("button", { name: "关闭对话框" }).click();

  await page.goto(`${baseURL}#serial`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "取得 Web TX", exact: true }).click();
  if (!(await page.getByRole("button", { name: "释放 Web TX", exact: true }).isVisible())) failures.push("interaction: Web TX lease state did not change");
  if (!(await page.getByLabel("串口发送内容").isEnabled())) failures.push("interaction: Web TX input stayed disabled");

  await page.goto(`${baseURL}#devices`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: /Lab Deck/ }).click();
  if (!(await page.getByText("Deck 已离线", { exact: true }).isVisible())) failures.push("interaction: offline Deck detail did not render");

  await page.goto(`${baseURL}#setup`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "失败", exact: true }).click();
  if (!(await page.getByRole("heading", { name: "新网络无法连接", exact: true }).isVisible())) failures.push("interaction: Setup failure state did not render");

  await page.goto(`${baseURL}#deck`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: /无 Provider 提示/ }).click();
  if (!(await page.getByRole("heading", { name: "添加 AI Provider", exact: true }).isVisible())) failures.push("interaction: Deck Provider hint did not render");
  await page.getByRole("button", { name: /串口统计子视图/ }).click();
  if (!(await page.getByText("流量健康", { exact: true }).isVisible())) failures.push("interaction: Deck serial stats did not render");

  await page.goto(`${baseURL}#backup`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "选择备份", exact: true }).click();
  await page.getByRole("button", { name: "解密并检查", exact: true }).click();
  if (!(await page.getByRole("heading", { name: "检查 3 项冲突" }).isVisible())) failures.push("interaction: backup conflict review did not render");

  await page.goto(`${baseURL}#diagnostics`, { waitUntil: "networkidle" });
  await page.getByRole("tab", { name: "事件日志" }).click();
  if (!(await page.getByText("AUTH_STALE", { exact: true }).isVisible())) failures.push("interaction: diagnostics event log did not render");

  await page.goto(`${baseURL}#login`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "已限流", exact: true }).click();
  if (!(await page.getByRole("heading", { name: "请在 00:42 后重试" }).isVisible())) failures.push("interaction: access rate-limited state did not render");

  await interactionContext.close();

  const shots = [
    ["overview", "desktop", 1440, 1000],
    ["provider-editor", "desktop", 1440, 1000],
    ["deck", "desktop", 1440, 1000],
    ["setup", "mobile", 375, 812],
    ["devices", "mobile", 375, 812],
    ["login", "mobile", 375, 812],
  ];
  for (const [route, name, width, height] of shots) {
    const context = await browser.newContext({ viewport: { width, height }, reducedMotion: "reduce", isMobile: name === "mobile" });
    const shotPage = await context.newPage();
    await shotPage.goto(`${baseURL}#${route}`, { waitUntil: "networkidle" });
    await shotPage.screenshot({ path: path.join(output, `C-${route}-${name}.png`), fullPage: true });
    await context.close();
  }

  await browser.close();
  if (failures.length) {
    console.error(failures.join("\n"));
    process.exitCode = 1;
  } else {
    console.log(`Verified ${routes.length * 2} responsive routes, ${(routes.length - 1) * exceptionalStates.length} exceptional states, and all key interactions.`);
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
