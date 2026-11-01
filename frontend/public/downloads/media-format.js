/** Shared by the page transports and the download Worker. */
const mediaExtensions = {
  "video/mp4": ["mp4", "m4v"], "video/webm": ["webm"], "video/quicktime": ["mov"],
  "video/x-matroska": ["mkv"], "video/mp2t": ["ts"],
  "audio/mpeg": ["mp3"], "audio/mp4": ["m4a", "mp4"], "audio/aac": ["aac"],
  "audio/ogg": ["ogg", "opus"], "audio/opus": ["opus"], "audio/wav": ["wav"],
  "audio/x-wav": ["wav"], "audio/flac": ["flac"],
  "image/jpeg": ["jpg", "jpeg"], "image/png": ["png"], "image/webp": ["webp"],
  "image/gif": ["gif"], "image/avif": ["avif"],
};
export const mediaTypes = Object.keys(mediaExtensions);
const safeExtensions = new Set(Object.values(mediaExtensions).flat());
const unsafeCharacter = /[\\/:*?"<>|\u0000-\u001f\u007f\u202a-\u202e\u2066-\u2069]/u;
const reservedName = /^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i;

export const mimeType = value => typeof value === "string" ? value.split(";", 1)[0].trim().toLowerCase() : "";
export const extensionOf = name => /\.([a-z0-9]+)$/i.exec(name)?.[1].toLowerCase() || "";
export const extensionsFor = type => {
  const key = mimeType(type);
  return Object.hasOwn(mediaExtensions, key) ? mediaExtensions[key] : [];
};

export function isSafeFilename(name) {
  return name.length > 0 && name.length <= 180 && name === name.trim()
    && !unsafeCharacter.test(name) && !/^[. ]|[. ]$/.test(name) && !reservedName.test(name);
}

export function cleanFilename(value) {
  return String(value || "download.bin").replace(new RegExp(unsafeCharacter, "gu"), "_").slice(0, 180).trim() || "download.bin";
}

/** Preserve an evidenced file format when the response can carry that type of media. */
export function attachmentFormat(filename, sourceType, formatMime, { allowUnknownMedia = false } = {}) {
  const observed = mimeType(sourceType) || "application/octet-stream";
  const generic = /^(?:application|binary)\/octet-stream$/.test(observed);
  if (!generic && !/^(?:video|audio|image)\//.test(observed))
    throw new Error("资源没有返回可下载的媒体文件");
  const requested = extensionOf(filename);
  const confirmed = extensionsFor(formatMime).includes(requested) ? mimeType(formatMime) : "";
  if (confirmed && !generic && confirmed.split("/")[0] !== observed.split("/")[0])
    throw new Error("来源媒体类型与已确认的文件格式不一致");
  const mime = confirmed || observed;
  const allowed = extensionsFor(mime);
  if (!allowUnknownMedia && !allowed.length && !(generic && safeExtensions.has(requested)))
    throw new Error("资源返回的类型不是可下载的音视频或图片");
  const suffix = allowed.includes(requested) ? requested : (allowed[0] || (safeExtensions.has(requested) ? requested : "bin"));
  const base = cleanFilename(filename).replace(/\.[^.]*$/, "").replace(/^[. ]+|[. ]+$/g, "").slice(0, 120);
  return { mime, filename: `${base && !reservedName.test(base) ? base : "media"}.${suffix}` };
}
