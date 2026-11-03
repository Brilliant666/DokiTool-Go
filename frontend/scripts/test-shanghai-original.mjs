// Feed actual Go adapter exports into the shipped selection/renewal consumer.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { ref } from "vue";

const [input] = process.argv.slice(2);
assert.ok(input, "Pass the JSON exported by TestShanghaiOriginalSelectionAndRefresh");
const responses = JSON.parse(await readFile(input, "utf8"));
const [dual, remaining, changedObject] = responses;
const server = await createServer({
  root: fileURLToPath(new URL("../", import.meta.url)),
  server: { middlewareMode: true, ws: false, hmr: false, watch: null },
  appType: "custom",
});
const previousFetch = globalThis.fetch;
try {
  const { validateMediaEnvelope } = await server.ssrLoadModule("/src/utils/mediaContract.ts");
  const { useVideoParser } = await server.ssrLoadModule("/src/composables/useVideoParser.ts");
  const { useVideoSelection } = await server.ssrLoadModule("/src/composables/useVideoSelection.ts");
  responses.forEach(response => validateMediaEnvelope(response));
  const data = ref(dual.data);
  const store = { get resultData() { return data.value; } };
  const selection = useVideoSelection({ videoStore: store });
  const parser = useVideoParser({ videoStore: store, locale: ref("zh-CN"), showToast() {} });
  selection.syncResult();
  const original = selection.currentVideo.value;
  const variant = original.variants.find(v => v.id === original.default.variant_id);
  assert.equal(variant.provenance, "original_endpoint");
  assert.equal(variant.delivery.sources.length, 2);
  assert.equal(selection.currentVideoUrl.value, original.default.url);
  const removed = variant.delivery.sources.find(s => s.id !== remaining.data.items[0].default.source_id);
  const renewed = variant.delivery.sources.find(s => s.id !== removed.id);
  let requests = 0;
  async function resolve(payload, source) {
    data.value = dual.data;
    const explicit = { variant_id: variant.id, source_id: source.id };
    selection.choose(explicit);
    globalThis.fetch = async url => {
      assert.ok(String(url).includes("/api/resolve?"));
      ++requests;
      return Response.json(payload);
    };
    const item = await parser.resolveItem(original);
    data.value = { ...dual.data, items: [item] };
    assert.deepEqual(selection.currentSelection.value, explicit);
    return selection.currentTarget.value;
  }
  const kept = await resolve(remaining, renewed);
  assert.equal(kept.kind, "file");
  assert.equal(kept.source.id, renewed.id);
  assert.notEqual(kept.url, renewed.url);
  assert.equal(kept.url, remaining.data.items[0].default.url);
  for (const [payload, source] of [[remaining, removed], [changedObject, renewed]]) {
    assert.ok(payload.data.items[0].default.url);
    const target = await resolve(payload, source);
    assert.equal(target.kind, "invalid");
    assert.equal(selection.currentVideoUrl.value, "");
  }
  assert.equal(requests, 3);
  console.log("SHANGHAI_ORIGINAL_CONSUMER_PASS: default, selected route renewal, missing route and replaced file; offline Go envelopes");
} finally {
  globalThis.fetch = previousFetch;
  await server.close();
}
