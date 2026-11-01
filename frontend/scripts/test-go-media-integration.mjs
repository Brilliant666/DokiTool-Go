// Test producer only: exercise unchanged frontend code with actual Go exports.
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { ref } from "vue";

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
const selectionCoverage = {};
// Consumer checks, not a replacement oracle. check.py independently compares
// these exact Go exports with immutable PHP before invoking this script.
function checkSelections(media, contract, useVideoSelection, label) {
  const count = selectionCoverage[media.platform] ??= {
    responses: 0, defaults: 0, explicit_sources: 0, rejected_missing_choices: 0,
    snapshot_replacements: 0, non_file_defaults: 0,
  };
  const now = media.generated_at ? Date.parse(media.generated_at) : 0;
  assert.ok(Number.isFinite(now), `${label}: deterministic generation clock`);
  const context = { now, apiOrigin: "https://dokitool.invalid" };
  const state = ref(structuredClone(media));
  const store = { get resultData() { return state.value; } };
  const selection = useVideoSelection({ videoStore: store });
  const realNow = Date.now;
  Date.now = () => now;
  try {
    selection.syncResult();
    assert.equal(selection.selectedItemId.value,
      media.default_item_id || (media.items.length === 1 ? media.items[0].id : ""),
      `${label}: server default item must not be re-ranked`);
    for (const item of media.items) {
      selection.selectItem(item.id);
      selection.resetDefault(item.id);
      assert.equal(selection.currentSelection.value, null, `${label}: default is not an explicit choice`);
      const defaultTarget = contract.getDownloadTarget(item, null, context);
      if (item.default) {
        assert.equal(defaultTarget.kind, "file", `${label}/${item.id}: default file`);
        assert.equal(defaultTarget.variant.id, item.default.variant_id);
        assert.equal(defaultTarget.source.url, item.default.url, "Signed default URL bytes changed");
        if (item.default.source_id) assert.equal(defaultTarget.source.id, item.default.source_id);
        count.defaults++;
      } else {
        assert.notEqual(defaultTarget.kind, "file", `${label}/${item.id}: do not invent a default`);
        count.non_file_defaults++;
      }
      // Object replacement is deliberately not called network renewal. It
      // exercises the original reactive selection store without syncResult(),
      // which would erase an explicit user choice and mask a renewal bug.
      const snapshot = () => JSON.stringify(selection.currentTarget.value);
      const replaceSnapshot = () => {
        const old = snapshot();
        const choice = JSON.stringify(selection.currentSelection.value);
        state.value = structuredClone(media);
        assert.equal(selection.selectedItemId.value, item.id);
        assert.equal(JSON.stringify(selection.currentSelection.value), choice);
        assert.equal(snapshot(), old, `${label}/${item.id}: replacement changed the selected identity`);
        count.snapshot_replacements++;
      };
      replaceSnapshot();
      if (item.state && item.state !== "ready") continue;
      let missing = "__go_consumer_missing_variant__";
      while (item.variants.some(variant => variant.id === missing)) missing += "_";
      assert.equal(contract.getDownloadTarget(item, { variant_id: missing }, context).kind,
        "invalid", `${label}: missing variant must not fall back to default`);
      count.rejected_missing_choices++;
      for (const variant of item.variants) {
        if (variant.state === "deferred" || variant.delivery.kind !== "file") continue;
        let missingSource = "__go_consumer_missing_source__";
        while (variant.delivery.sources.some(source => source.id === missingSource)) missingSource += "_";
        assert.equal(contract.getDownloadTarget(item, { variant_id: variant.id, source_id: missingSource }, context).kind,
          "invalid", `${label}: missing source must not silently change identity`);
        count.rejected_missing_choices++;
        for (const source of variant.delivery.sources) {
          // Sources without IDs are permitted only as the sole source. Never
          // fabricate an identity in order to make the consumer test pass.
          assert.ok(source.id || variant.delivery.sources.length === 1, `${label}: explicit source identity`);
          const choice = { variant_id: variant.id, ...(source.id ? { source_id: source.id } : {}) };
          const target = contract.getDownloadTarget(item, choice, context);
          const kind = source.availability === "unavailable" ? "unavailable"
            : source.expires_at && Date.parse(source.expires_at) <= now ? "needs_refresh" : "file";
          assert.equal(target.kind, kind, `${label}/${item.id}/${variant.id}: source state`);
          if (kind === "file") {
            assert.equal(target.item.id, item.id);
            assert.equal(target.variant.id, variant.id);
            assert.equal(target.source.id, source.id);
            assert.equal(target.url, source.access === "relay" ? context.apiOrigin + source.url : source.url,
              `${label}: selected URL must preserve every signed query byte`);
            assert.equal(target.routes[0], target.url, `${label}: explicit source must stay first`);
          }
          selection.choose(choice, item.id);
          assert.deepEqual(selection.currentSelection.value, choice);
          replaceSnapshot();
          count.explicit_sources++;
        }
      }
      selection.resetDefault(item.id);
      assert.equal(selection.currentSelection.value, null);
    }
    count.responses++;
  } finally {
    Date.now = realNow;
  }
}
const vite = await createServer({
  root,
  server: { middlewareMode: true, ws: false, hmr: false, watch: null },
  appType: "custom",
});
try {
  const contract = await vite.ssrLoadModule("/src/utils/mediaContract.ts");
  const { validateMediaEnvelope } = contract;
  const { useVideoSelection } = await vite.ssrLoadModule("/src/composables/useVideoSelection.ts");
  for (const entry of cases.values()) for (const view of ["normal", "debug"]) {
    const envelope = entry[view];
    const before = JSON.stringify(envelope);
    if (envelope.code === "OK") {
      try {
        assert.equal(validateMediaEnvelope(envelope), envelope.data, `${entry.id}/${view}`);
        checkSelections(envelope.data, contract, useVideoSelection, `${entry.id}/${view}`);
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

for (const platform of ["douyin", "bilibili", "weibo", "toutiao", "kuaishou", "pipixia", "pipigx", "zuiyou", "xiaohongshu"]) {
  const coverage = selectionCoverage[platform];
  assert.ok(coverage?.responses > 0 && coverage.defaults > 0 && coverage.explicit_sources > 0
    && coverage.rejected_missing_choices > 0 && coverage.snapshot_replacements > 0,
    `Actual Go default/explicit consumer coverage missing for ${platform}`);
}
assert.equal(Object.values(selectionCoverage).reduce((sum, value) => sum + value.responses, 0), accepted,
  "Every accepted Go response must reach the unchanged selection consumer");

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
    selection_coverage: selectionCoverage,
    snapshot_scope: "Same-response object replacement only; not a platform network renewal or browser save",
    rejected_successes: failures.length, failures,
    evidence: "Actual Go envelopes consumed by unchanged frontend validator, selection and download composables; mocked transfer bytes, no browser/file-save claim",
  };
  if (report) await writeFile(report, JSON.stringify(outcome, null, 2) + "\n");
  console.log(JSON.stringify(outcome, null, 2));
  if (!outcome.passed) process.exitCode = 1;
} finally {
  await rm(temporary, { recursive: true, force: true });
}
