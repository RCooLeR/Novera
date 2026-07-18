import { readFile } from "node:fs/promises";

const html = await readFile(new URL("../dist/index.html", import.meta.url), "utf8");
const cspMetaTags = [...html.matchAll(/<meta\b[^>]*>/gi)]
  .map((match) => match[0])
  .filter((tag) => /\bhttp-equiv\s*=\s*["']Content-Security-Policy["']/i.test(tag));

if (cspMetaTags.length !== 1) {
  throw new Error(`production index.html must contain exactly one Content-Security-Policy meta tag; found ${cspMetaTags.length}`);
}

// The source template uses a double-quoted attribute because CSP source
// expressions themselves contain single quotes (for example, 'self').
const policy = cspMetaTags[0].match(/\bcontent\s*=\s*"([^"]*)"/i)?.[1];
if (!policy) {
  throw new Error("production Content-Security-Policy meta tag has no double-quoted content attribute");
}

const directives = new Map();
for (const sourceDirective of policy.split(";")) {
  const tokens = sourceDirective.trim().split(/\s+/).filter(Boolean);
  if (tokens.length === 0) continue;
  const [name, ...values] = tokens;
  if (directives.has(name)) {
    throw new Error(`production Content-Security-Policy contains duplicate ${name} directives`);
  }
  directives.set(name, values);
}

const expectedDirectives = new Map([
  ["default-src", ["'self'"]],
  ["script-src", ["'self'"]],
  ["connect-src", ["'self'"]],
  ["style-src", ["'self'", "'unsafe-inline'"]],
  ["img-src", ["'self'", "data:", "blob:"]],
  ["font-src", ["'self'", "data:"]],
  ["worker-src", ["'self'", "blob:"]],
  ["object-src", ["'none'"]],
  ["base-uri", ["'self'"]],
  ["form-action", ["'none'"]],
]);

for (const [name, expectedValues] of expectedDirectives) {
  const actualValues = directives.get(name);
  if (
    !actualValues ||
    actualValues.length !== expectedValues.length ||
    expectedValues.some((value) => !actualValues.includes(value))
  ) {
    throw new Error(
      `production ${name} does not match the required policy: ${(actualValues ?? ["missing"]).join(" ")}`,
    );
  }
}

const unexpectedDirectives = [...directives.keys()].filter((name) => !expectedDirectives.has(name));
if (unexpectedDirectives.length > 0) {
  throw new Error(`production Content-Security-Policy contains unexpected directives: ${unexpectedDirectives.join(", ")}`);
}

console.log("production CSP check passed");
