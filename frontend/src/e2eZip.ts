// Sprag - a post-quantum-safe end-to-end encrypted file dropbox.
// Copyright (C) 2026 Tobias von Dewitz <tobias@vondewitz.org>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

import { Zip, ZipPassThrough } from "fflate";

// Client-side analogs of the Go helpers in internal/http/server.go
// (sanitizeFilename / uniqueZipName / safeArchiveName), so the E2E zip
// layout and download name match the plaintext server zip.

function sanitizeFilename(name: string): string {
  let cleaned = name.replace(/\\/g, "/").split("/").pop() ?? "";
  cleaned = cleaned
    .split("")
    .filter((char) => {
      const code = char.charCodeAt(0);
      return code >= 32 && code !== 127 && char !== "/" && char !== "\\";
    })
    .join("");
  cleaned = cleaned.trim();
  if (cleaned === "" || cleaned === "." || cleaned === "..") return "file";
  return cleaned;
}

// Returns a name not yet present in used and records it. When the sanitized
// name is taken it appends -2, -3, ... and re-checks each candidate, so a
// generated suffix can never collide with another entry's real name.
export function uniqueZipName(used: Set<string>, original: string): string {
  const name = sanitizeFilename(original);
  if (!used.has(name)) {
    used.add(name);
    return name;
  }
  const dot = name.lastIndexOf(".");
  const ext = dot >= 0 ? name.slice(dot) : "";
  const base = dot >= 0 ? name.slice(0, dot) : name;
  for (let n = 2; ; n += 1) {
    const candidate = `${base}-${n}${ext}`;
    if (!used.has(candidate)) {
      used.add(candidate);
      return candidate;
    }
  }
}

// Archive filename: sanitized title with extension stripped, spaces as
// dashes, lowercased, leading/trailing .-_ trimmed (matches Go safeArchiveName).
export function safeArchiveName(name: string): string {
  let out = sanitizeFilename(name);
  const dot = out.lastIndexOf(".");
  if (dot >= 0) out = out.slice(0, dot);
  out = out.replace(/ /g, "-").toLowerCase();
  return out.replace(/^[.\-_]+|[.\-_]+$/g, "");
}

// Build a flat, uncompressed (Store) zip of the given already-decrypted
// blobs. Mirrors the server's plaintext zip. Names are deduped like the Go
// uniqueZipName. Streams from the disk-backed blobs so memory stays bounded.
export async function buildDecryptedZip(
  files: { name: string; blob: Blob }[]
): Promise<Blob> {
  const chunks: Uint8Array<ArrayBuffer>[] = [];
  let zipError: Error | null = null;
  const zip = new Zip((err, dat) => {
    if (err) zipError = err;
    if (dat) chunks.push(new Uint8Array(dat));
  });
  try {
    const used = new Set<string>();
    for (const file of files) {
      const name = uniqueZipName(used, file.name);
      const member = new ZipPassThrough(name);
      zip.add(member);
      await streamIntoMember(member, file.blob.stream());
    }
  } finally {
    zip.end();
  }
  if (zipError) throw zipError;
  return new Blob(chunks, { type: "application/zip" });
}

async function streamIntoMember(member: ZipPassThrough, stream: ReadableStream<Uint8Array>): Promise<void> {
  const reader = stream.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      member.push(value, false);
    }
  } finally {
    reader.releaseLock();
  }
  member.push(new Uint8Array(), true);
}
