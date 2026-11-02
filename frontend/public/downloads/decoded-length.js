/** Size of the file bytes that Fetch exposes to its reader. A CORS response
 * can expose Content-Length while hiding Content-Encoding. */
export function decodedLength(response, sourceSize = 0) {
  const encoding = response.headers.get("content-encoding")?.trim().toLowerCase();
  const identity = encoding === "identity" || (!encoding && response.type !== "cors");
  const length = identity ? Number(response.headers.get("content-length")) : 0;
  const declared = Number.isSafeInteger(length) && length > 0 ? length : 0;
  if (!Number.isSafeInteger(sourceSize) || sourceSize <= 0) return declared;
  if (declared && declared !== sourceSize) throw new Error("下载长度与来源声明不一致");
  return sourceSize;
}
