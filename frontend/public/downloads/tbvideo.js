/* Some COS edges expose the object ETag and length but hide Content-Range.
 * Their quoted hex ETag is the MD5 of the complete, unwatermarked file.
 * WebCrypto has no MD5. This RFC 1321 checksum verifies object integrity only. */
const shifts = [7, 12, 17, 22, 5, 9, 14, 20, 4, 11, 16, 23, 6, 10, 15, 21];
const constants = Int32Array.from({ length: 64 }, (_, i) => Math.floor(Math.abs(Math.sin(i + 1)) * 2 ** 32));

function compress(state, view, offset) {
  let [a, b, c, d] = state;
  for (let i = 0; i < 64; i++) {
    let f, k;
    if (i < 16) { f = (b & c) | (~b & d); k = i; }
    else if (i < 32) { f = (d & b) | (~d & c); k = (5 * i + 1) % 16; }
    else if (i < 48) { f = b ^ c ^ d; k = (3 * i + 5) % 16; }
    else { f = c ^ (b | ~d); k = (7 * i) % 16; }
    const n = (a + f + constants[i] + view.getInt32(offset + k * 4, true)) | 0;
    const shift = shifts[(i >>> 4) * 4 + (i % 4)];
    a = d; d = c; c = b; b = (b + ((n << shift) | (n >>> (32 - shift)))) | 0;
  }
  state[0] += a; state[1] += b; state[2] += c; state[3] += d;
}

class ObjectMD5 {
  state = new Int32Array([0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476]);
  tail = new Uint8Array(64);
  length = 0;

  update(bytes) {
    let offset = 0;
    const prev = this.length % 64;
    this.length += bytes.length;
    if (prev) {
      const used = Math.min(64 - prev, bytes.length);
      this.tail.set(bytes.subarray(0, used), prev);
      if (prev + used < 64) return;
      compress(this.state, new DataView(this.tail.buffer), 0);
      offset = used;
    }
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    for (; offset + 64 <= bytes.length; offset += 64) compress(this.state, view, offset);
    this.tail.set(bytes.subarray(offset));
  }

  hex() {
    const remaining = this.length % 64;
    const tail = new Uint8Array(remaining < 56 ? 64 : 128);
    tail.set(this.tail.subarray(0, remaining)); tail[remaining] = 0x80;
    const view = new DataView(tail.buffer);
    view.setUint32(tail.length - 8, this.length * 8, true);
    view.setUint32(tail.length - 4, Math.floor(this.length / 0x20000000), true);
    const state = this.state.slice();
    for (let i = 0; i < tail.length; i += 64) compress(state, view, i);
    const result = new DataView(new ArrayBuffer(16));
    state.forEach((word, i) => result.setInt32(4 * i, word, true));
    return Array.from(new Uint8Array(result.buffer), byte => byte.toString(16).padStart(2, "0")).join("");
  }
}

const RANGE_BYTES = 4 * 1024 * 1024;

export function validCandidate(value) {
  const url = new URL(value);
  return url.origin === "https://tbvideo.ixiaochuan.cn" && !url.username && !url.password &&
    !url.search && !url.hash && url.pathname.length > 1 && !url.pathname.startsWith("//");
}

function lengthHeader(response) {
  const raw = response.headers.get("Content-Length");
  const length = Number(raw);
  if (!/^[1-9][0-9]*$/.test(raw || "") || !Number.isSafeInteger(length)) throw new Error("invalid length");
  return length;
}

function objectTag(response) {
  const etag = /^"([a-f0-9]{32})"$/i.exec(response.headers.get("ETag") || "");
  if (!etag) throw new Error("unverifiable object");
  return etag[1].toLowerCase();
}

// Require exact offsets if exposed. Otherwise enforce requested byte counts,
// stable ETag and complete-file MD5; never infer a successful GET from OPTIONS.
function rangeTotal(response, start, requestedEnd, length, total) {
  const range = response.headers.get("Content-Range");
  if (range !== null) {
    const match = /^bytes ([0-9]+)-([0-9]+)\/([0-9]+)$/.exec(range);
    if (!match) throw new Error("invalid range");
    const size = Number(match[3]);
    if (!Number.isSafeInteger(size) || size <= start || (total && size !== total) ||
        Number(match[1]) !== start || Number(match[2]) !== start + length - 1) throw new Error("changed range");
    total = size;
  }
  if (length > requestedEnd - start + 1 ||
      (total && length !== Math.min(total - 1, requestedEnd) - start + 1)) throw new Error("unexpected range length");
  return total;
}

export async function readCandidate(url, signal, timed, progress) {
  if (!validCandidate(url)) throw new Error("invalid candidate");
  const request = options => timed(() => fetch(url, { mode: "cors", credentials: "omit", cache: "no-store",
    redirect: "error", referrerPolicy: "no-referrer", signal, ...options }));
  const checksum = new ObjectMD5();
  const parts = [];
  let tag, total = 0;
  let verified = false;
  do {
    const start = checksum.length;
    const end = Math.min(start + RANGE_BYTES - 1, total ? total - 1 : Number.MAX_SAFE_INTEGER);
    const response = await request({ headers: { Range: `bytes=${start}-${end}` } });
    const reader = response.body?.getReader();
    let ended = false;
    try {
      if (!reader || response.status !== 206 || response.type === "opaque") throw new Error("unreadable range");
      const length = lengthHeader(response);
      const currentTag = objectTag(response);
      if (tag && currentTag !== tag) throw new Error("object changed");
      tag = currentTag;
      total = rangeTotal(response, start, end, length, total);
      const chunks = [];
      for (;;) {
        const { done, value } = await timed(() => reader.read());
        if (done) break;
        if (checksum.length - start + value.length > length) throw new Error("range overrun");
        checksum.update(value);
        chunks.push(value);
        progress(checksum.length, total);
      }
      ended = true;
      if (checksum.length - start !== length) throw new Error("truncated range");
      parts.push(new Blob(chunks));
    } finally {
      if (reader) {
        if (!ended) void reader.cancel().catch(() => {});
        reader.releaseLock();
      }
    }

    if (!total) {
      // The first bounded GET may already contain the full object. Only consult
      // HEAD when it fills the range and its digest does not match the whole.
      verified = checksum.hex() === tag;
      if (verified || checksum.length < RANGE_BYTES) total = checksum.length;
      else {
        const head = await request({ method: "HEAD" });
        if (head.status !== 200 || objectTag(head) !== tag) throw new Error("object changed");
        total = lengthHeader(head);
        if (total <= checksum.length) throw new Error("object mismatch");
      }
    }
  } while (checksum.length < total);

  if (checksum.length !== total || (!verified && checksum.hex() !== tag)) throw new Error("object mismatch");
  const file = new Blob(parts, { type: "video/mp4" });
  const prefix = new Uint8Array(await file.slice(0, 12).arrayBuffer());
  if (file.size !== total || prefix.length < 12 || String.fromCharCode(...prefix.subarray(4, 8)) !== "ftyp") {
    throw new Error("invalid MP4");
  }
  progress(total, total);
  return file;
}

// Report only numeric upstream identities and aggregate transfer data. A saved
// result means a verified complete stream was handed to the browser.
export async function reportTbvideo(event, snapshot) {
  if (!event || !["pipigx", "zuiyou"].includes(event.platform) ||
      ![event.pid, event.vid].every(id => typeof id === "string" && /^[0-9]{1,32}$/.test(id)) ||
      !["completed", "manual"].includes(snapshot.status)) return;
  const body = JSON.stringify({ platform: event.platform, pid: event.pid, vid: event.vid,
    used_candidate: snapshot.tbvideoAttempted === true, result: snapshot.status === "completed" ? "saved" : "fallback",
    total_bytes: snapshot.loaded, elapsed_ms: Math.round(snapshot.elapsed * 1000) });
  try {
    await fetch("/api/downloads/tbvideo-result", { method: "POST", mode: "same-origin", credentials: "omit",
      headers: { "Content-Type": "application/json" }, referrerPolicy: "no-referrer", body, keepalive: true });
  } catch { /* Reporting is best effort and must never start or block a download. */ }
}
