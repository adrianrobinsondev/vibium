import { browser } from "../clients/javascript/dist/index.mjs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outPath = path.resolve(__dirname, "..", "two-tabs-record.zip");

const bro = await browser.start();
const tab1 = await bro.page();

await tab1.setViewport({ width: 1280, height: 720 });

await tab1.context.recording.start({
    name: "two-tabs",
    title: "Two Tabs Test",
    screenshots: true,
    snapshots: true,
});

// Tab 1: example.com
await tab1.context.recording.startGroup("Tab 1 — example.com");
await tab1.go("https://example.com");
await tab1.wait(500);
await tab1.context.recording.stopGroup();

// Tab 2: var.parts
await tab1.context.recording.startGroup("Tab 2 — var.parts");
const tab2 = await tab1.context.newPage();
await tab2.setViewport({ width: 1280, height: 720 });
await tab2.go("https://var.parts");
await tab2.wait(500);
await tab2.context.recording.stopGroup();

// Switch back to tab 1, interact a bit
await tab1.context.recording.startGroup("Back to Tab 1");
await tab1.go("https://example.com");
await tab1.wait(500);
await tab1.context.recording.stopGroup();

// Stop recording & save
await tab1.context.recording.stop({ path: outPath });
console.log(`Recording saved → ${outPath}`);

await bro.stop();
