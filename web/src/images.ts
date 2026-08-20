/**
 * Turning whatever somebody picked into something worth storing.
 *
 * Conversion happens in the browser rather than on the server, and that is a
 * deliberate trade. Go has no WebP encoder in its standard library, so doing
 * this server-side means a third-party image codec sitting in the path of an
 * uploaded file — which is a large amount of C, or a large amount of new Go, to
 * add to a product whose selling point is how few moving parts it has. Every
 * browser already ships an encoder that is better than either.
 *
 * It also means the bytes are smaller before they leave the machine, so the
 * upload, the row and the response all shrink together.
 *
 * The server does not trust any of this. It still checks the content type
 * against its own allowlist, because a browser is not the only thing that can
 * make a PUT.
 */

/** What comes back: the bytes to send, and what to call them. */
export type Converted = { blob: Blob; type: string };

/**
 * Rasterise and re-encode an image as WebP, scaled to fit within `maxPx` on its
 * longest side.
 *
 * Animated GIFs are passed through untouched. A canvas only ever sees the first
 * frame, so converting one would silently turn an animation into a still — a
 * surprise nobody asked for, and worth more than the bytes it would save.
 *
 * Anything that cannot be decoded comes back unchanged and the server decides
 * what to do with it, which keeps a browser quirk from making an upload
 * impossible.
 */
export async function toWebp(file: File, maxPx: number): Promise<Converted> {
  if (file.type === "image/gif") return { blob: file, type: file.type };

  try {
    const bitmap = await load(file);
    const scale = Math.min(1, maxPx / Math.max(bitmap.width, bitmap.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(bitmap.width * scale));
    canvas.height = Math.max(1, Math.round(bitmap.height * scale));

    const ctx = canvas.getContext("2d");
    if (!ctx) return { blob: file, type: file.type };
    ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);

    const encoded = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, "image/webp", 0.85),
    );
    if (!encoded) return { blob: file, type: file.type };

    // A tiny flat PNG can beat WebP. Keep whichever is actually smaller, unless
    // the source was an SVG — that one has to be rasterised whatever the size,
    // because the server will not store markup.
    if (encoded.size >= file.size && file.type !== "image/svg+xml") {
      return { blob: file, type: file.type };
    }
    return { blob: encoded, type: "image/webp" };
  } catch {
    return { blob: file, type: file.type };
  }
}

/**
 * Decode a file into something drawable.
 *
 * SVG goes through an <img> rather than createImageBitmap, which does not
 * accept it. An SVG with no intrinsic size draws as nothing, so one is given a
 * square to fill — a mark exported without width and height is common enough to
 * be worth handling rather than failing on.
 */
async function load(file: File): Promise<ImageBitmap | HTMLImageElement> {
  if (file.type !== "image/svg+xml") return createImageBitmap(file);

  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    if (!img.naturalWidth || !img.naturalHeight) {
      img.width = 1024;
      img.height = 1024;
    }
    return img;
  } finally {
    // Revoked after decode: the bitmap is drawn from the decoded image, not
    // from the URL, so the object is no longer needed.
    URL.revokeObjectURL(url);
  }
}
