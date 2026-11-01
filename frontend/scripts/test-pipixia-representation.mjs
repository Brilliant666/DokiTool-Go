// Consume real Go parser exports; never invent IDs or reselect a new default.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { ref } from "vue";

const [input] = process.argv.slice(2);
assert.ok(input, "Pass DOKITOOL_PIPIXIA_REPRESENTATION_OUTPUT from the Go test");
const cases = JSON.parse(await readFile(input, "utf8"));
const root = fileURLToPath(new URL("../", import.meta.url));
const vite = await createServer({
  root, server: { middlewareMode: true, ws: false, hmr: false, watch: null }, appType: "custom",
});
const previousFetch = globalThis.fetch;
try {
  const { validateMediaEnvelope, getDownloadTarget } = await vite.ssrLoadModule("/src/utils/mediaContract.ts");
  const { useVideoSelection } = await vite.ssrLoadModule("/src/composables/useVideoSelection.ts");
  const { useVideoParser } = await vite.ssrLoadModule("/src/composables/useVideoParser.ts");
  for (const payload of Object.values(cases)) validateMediaEnvelope(payload);
  const before = cases.before.data;
  const originalItem = before.items[0];
  const variant = originalItem.variants[0];
  const source = variant.delivery.sources[0];
  const explicit = { variant_id: variant.id, source_id: source.id };
  let calls = 0;
  for (const [name, expectedKind] of [["added", "file"], ["removed", "file"], ["source-missing", "invalid"], ["different-file", "invalid"]]) {
    const state = ref(structuredClone(before));
    const videoStore = { get resultData() { return state.value; }, clearResult() {}, setResult(data) { state.value = data; } };
    const selection = useVideoSelection({ videoStore });
    selection.syncResult();
    selection.choose(explicit);
    const parser = useVideoParser({ videoStore, locale: ref("zh-CN"), showToast() {} });
    // Only the HTTP transport is substituted with actual Go output. The real
    // resolveItem validation/lookup and existing selection consumer still run.
    globalThis.fetch = async (url) => {
      assert.ok(String(url).includes("/api/resolve?"));
      calls++;
      return new Response(JSON.stringify(cases[name]), { status: 200, headers: { "Content-Type": "application/json" } });
    };
    const resolved = await parser.resolveItem(originalItem);
    assert.equal(resolved.id, originalItem.id);
    assert.equal(getDownloadTarget(resolved, explicit).kind, expectedKind, name);
    state.value = { ...state.value, items: state.value.items.map(item => item.id === resolved.id ? resolved : item) };
    assert.deepEqual(selection.currentSelection.value, explicit, "resolve must not rewrite user choice");
    assert.equal(selection.currentTarget.value.kind, expectedKind, name);
    if (expectedKind === "file") {
      assert.equal(selection.currentTarget.value.source.id, source.id);
      assert.equal(selection.currentTarget.value.source.url, source.url, "original signed bytes retained");
    } else {
      assert.ok(resolved.default, "negative case has another usable server default");
      assert.equal(selection.currentVideoUrl.value, "", "missing identity must not borrow that default");
    }
  }
  assert.equal(calls, 4);
  console.log("PIPIXIA_REPRESENTATION_CONSUMER_PASS: 4 actual Go responses through resolveItem/selection; no browser or media save");
} finally {
  globalThis.fetch = previousFetch;
  await vite.close();
}
