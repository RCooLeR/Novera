import { readFile } from "node:fs/promises";

const [html, manifestSource] = await Promise.all([
  readFile(new URL("../dist/index.html", import.meta.url), "utf8"),
  readFile(new URL("../dist/.vite/manifest.json", import.meta.url), "utf8"),
]);

const manifest = JSON.parse(manifestSource);
const records = Object.entries(manifest);
const entryRecords = records.filter(([, record]) => record.isEntry === true);

if (entryRecords.length !== 1) {
  throw new Error(`expected exactly one production entry in the Vite manifest, found ${entryRecords.length}`);
}

const [entryKey, entryRecord] = entryRecords[0];
const expectedDynamicEntries = ["EditorPane", "TerminalView"].map((name) => {
  const matches = records.filter(([, record]) => record.name === name);
  if (matches.length !== 1) {
    throw new Error(`expected exactly one ${name} record in the Vite manifest, found ${matches.length}`);
  }
  const [key, record] = matches[0];
  if (record.isDynamicEntry !== true) {
    throw new Error(`${name} is no longer a dynamic production entry`);
  }
  return [key, record];
});

function collectGraph(startKeys, edgeNames) {
  const visited = new Set();
  const pending = [...startKeys];
  while (pending.length > 0) {
    const key = pending.pop();
    if (visited.has(key)) continue;
    const record = manifest[key];
    if (!record) {
      throw new Error(`Vite manifest references missing record ${key}`);
    }
    visited.add(key);
    for (const edgeName of edgeNames) {
      pending.push(...(record[edgeName] ?? []));
    }
  }
  return visited;
}

const isHeavyRecord = (key, record) =>
  /EditorPane|TerminalView|monaco|xterm/i.test(
    [key, record.name, record.src, record.file].filter(Boolean).join(" "),
  );

const initialGraph = collectGraph([entryKey], ["imports"]);
const eagerHeavyRecords = [...initialGraph].filter((key) => isHeavyRecord(key, manifest[key]));
if (eagerHeavyRecords.length > 0) {
  throw new Error(`production entry statically imports heavy feature records: ${eagerHeavyRecords.join(", ")}`);
}

const reachableGraph = collectGraph([entryKey], ["imports", "dynamicImports"]);
for (const [key, record] of expectedDynamicEntries) {
  if (!reachableGraph.has(key)) {
    throw new Error(`${record.name} is emitted but is not reachable from the production entry`);
  }
}

const normalizeAsset = (asset) => asset.split(/[?#]/, 1)[0].replace(/^(?:\.\/|\/)+/, "");
const initialAssets = new Set(
  [...html.matchAll(/\b(?:src|href)=["']([^"']+)["']/gi)].map((match) => normalizeAsset(match[1])),
);
if (!initialAssets.has(normalizeAsset(entryRecord.file))) {
  throw new Error(`production index.html does not load the manifest entry ${entryRecord.file}`);
}

const heavyAssets = new Set();
for (const [key, record] of records) {
  if (!isHeavyRecord(key, record)) continue;
  for (const asset of [record.file, ...(record.css ?? []), ...(record.assets ?? [])]) {
    if (asset) heavyAssets.add(normalizeAsset(asset));
  }
}
const eagerHeavyAssets = [...initialAssets].filter((asset) => heavyAssets.has(asset));
if (eagerHeavyAssets.length > 0) {
  throw new Error(`production index.html eagerly loads editor/terminal assets: ${eagerHeavyAssets.join(", ")}`);
}

console.log("production entry lazy-load check passed");
