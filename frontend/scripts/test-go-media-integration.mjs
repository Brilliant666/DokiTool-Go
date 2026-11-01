// Test producer only: exercise unchanged frontend code with actual Go exports.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const [input, report] = process.argv.slice(2);
assert.ok(input, "Pass the actual DOKITOOL_GO_DOUYIN_OUTPUT JSON export");
const root = fileURLToPath(new URL("../", import.meta.url));
const exported = JSON.parse(await readFile(input, "utf8"));
const cases = new Map(exported.cases.map(entry => [entry.id, entry]));
assert.equal(cases.size, exported.cases.length, "Duplicate Go output identities");
const php = JSON.parse(await readFile(new URL("../../backend/testdata/php-douyin/cases.json", import.meta.url), "utf8"));
const integerPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/douyin/testdata/php-integer-boundaries.json", import.meta.url), "utf8"));
const currentPhp = JSON.parse(await readFile(new URL("../../backend/testdata/php-25beaee/cases.json", import.meta.url), "utf8"));
const bilibiliPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/bilibili/testdata/php-baseline.json", import.meta.url), "utf8"));
const weiboPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/weibo/testdata/php-signed-baseline.json", import.meta.url), "utf8"));
const toutiaoPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/toutiao/testdata/php-baseline.json", import.meta.url), "utf8"));
const kuaishouPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/kuaishou/testdata/php-baseline.json", import.meta.url), "utf8"));
const pipixiaPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/pipixia/testdata/php-baseline.json", import.meta.url), "utf8"));
const pipigxPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/pipigx/testdata/php-baseline.json", import.meta.url), "utf8"));
const zuiyouPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/zuiyou/testdata/php-baseline.json", import.meta.url), "utf8"));
const xiaohongshuPhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/xiaohongshu/testdata/php-baseline.json", import.meta.url), "utf8"));
const xiaohongshuWirePhp = JSON.parse(await readFile(new URL("../../backend/internal/platform/xiaohongshu/testdata/php-wire-baseline.json", import.meta.url), "utf8"));
// Zuiyou's normal/debug are the final real gateway parse/resolve envelopes.
// Consume them directly, just as the signed Weibo gateway fixture is consumed.
const phpEntries = [...php.cases, ...integerPhp.cases, ...currentPhp.cases, ...bilibiliPhp.cases,
  ...weiboPhp.cases, ...toutiaoPhp.cases, ...kuaishouPhp.cases, ...pipixiaPhp.cases,
  ...pipigxPhp.cases, ...zuiyouPhp.cases, ...xiaohongshuPhp.cases, ...xiaohongshuWirePhp.cases];
const phpCases = new Map(phpEntries.map(entry => [entry.id, entry.expected]));
assert.equal(phpCases.size, phpEntries.length, "Duplicate frozen PHP output identities");
assert.deepEqual([...cases.keys()].sort(), [...phpCases.keys()].sort(), "Actual Go frontend inventory must contain every frozen case");
let accepted = 0, rejected = 0;
const failures = [];
const vite = await createServer({
  root,
  server: { middlewareMode: true, ws: false, hmr: false, watch: null },
  appType: "custom",
});
try {
  const { validateMediaEnvelope } = await vite.ssrLoadModule("/src/utils/mediaContract.ts");
  for (const entry of cases.values()) for (const view of ["normal", "debug"]) {
    const envelope = entry[view];
    const before = JSON.stringify(envelope);
    if (envelope.code === "OK") {
      try {
        assert.equal(validateMediaEnvelope(envelope), envelope.data, `${entry.id}/${view}`);
        accepted++;
      } catch (error) {
        // Diagnose inherited contract conflicts without suppressing this failure.
        const reference = phpCases.get(entry.id)?.[view];
        let same = false, phpReason = null;
        try { assert.deepEqual(reference, envelope); same = true; } catch {}
        if (reference) try { validateMediaEnvelope(reference); } catch (phpError) { phpReason = phpError.message; }
        failures.push({ id: `${entry.id}/${view}`, reason: error.message,
          identical_php_response: same, php_rejection: phpReason });
      }
    } else {
      assert.throws(() => validateMediaEnvelope(envelope),
        error => error.message === envelope.error.message, `${entry.id}/${view}`);
      rejected++;
    }
    assert.equal(JSON.stringify(envelope), before, "Validation mutated the Go response");
  }
} finally {
  await vite.close();
}

const temporary = await mkdtemp(join(tmpdir(), "dokitool-go-frontend-"));
try {
  const before = id => {
    assert.ok(cases.has(id), `Missing actual Go renewal output: ${id}`);
    return cases.get(id).normal;
  };
  const path = join(temporary, "renewal.json");
  const weiboPath = join(temporary, "weibo-renewal.json");
  await writeFile(weiboPath, JSON.stringify({
    before: before("weibo/renewal/before/parse"), after: before("weibo/renewal/after/resolve"),
  }));
  await writeFile(path, JSON.stringify({
    before: before("renewal/play/before"), after: before("renewal/play/after"),
    cases: { cdn: { before: before("renewal/cdn/before"), after: before("renewal/cdn/after") } },
  }));
  const result = spawnSync(process.execPath, [
    "--test", "--test-reporter=tap", "--test-concurrency=1",
    "--test-name-pattern=^(PR8 integration: actual (play-endpoint|cdn) adapter renewal preserves|Go Weibo integration:)",
    "tests/ui-download-contract.test.mjs",
  ], {
    cwd: root, encoding: "utf8", maxBuffer: 8 * 1024 * 1024,
    env: { ...process.env, DOKITOOL_MEDIA_INTEGRATION: "1", DOKITOOL_RENEWAL_FIXTURE: path, DOKITOOL_WEIBO_RENEWAL_FIXTURE: weiboPath },
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`Existing frontend renewal regressions failed\n${result.stdout}\n${result.stderr}`);
  const passes = [...result.stdout.matchAll(/^ok \d+ - PR8 integration: actual (?:play-endpoint|cdn) adapter renewal preserves (?:explicit|default) download identity\s*$/gm)];
  assert.equal(passes.length, 4, "All four existing renewal tests must execute and pass");
  const weiboPasses = [...result.stdout.matchAll(/^ok \d+ - Go Weibo integration: (?:expired-renewal|applied-refresh) preserves (?:explicit|default) signed download identity\s*$/gm)];
  assert.equal(weiboPasses.length, 4, "All four signed Weibo selection/renewal tests must execute and pass");
  const outcome = {
    passed: failures.length === 0, responses: accepted + rejected + failures.length, valid_successes: accepted,
    expected_errors: rejected, unchanged_renewal_tests: passes.length,
    weibo_signed_renewal_tests: weiboPasses.length,
    rejected_successes: failures.length, failures,
    evidence: "Actual Go envelopes consumed by unchanged frontend validator, selection and download composables; mocked transfer bytes, no browser/file-save claim",
  };
  if (report) await writeFile(report, JSON.stringify(outcome, null, 2) + "\n");
  console.log(JSON.stringify(outcome, null, 2));
  if (!outcome.passed) process.exitCode = 1;
} finally {
  await rm(temporary, { recursive: true, force: true });
}
