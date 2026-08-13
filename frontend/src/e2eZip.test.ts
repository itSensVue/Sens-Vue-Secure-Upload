/*
 * Sprag - a post-quantum-safe end-to-end encrypted file dropbox.
 * Copyright (C) 2026 Tobias von Dewitz <tobias@vondewitz.org>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

import { describe, expect, it } from "vitest";
import { strFromU8, unzipSync } from "fflate";
import { buildDecryptedZip, safeArchiveName, uniqueZipName } from "./e2eZip";

function blob(name: string, content: string): { name: string; blob: Blob } {
  return { name, blob: new Blob([content]) };
}

describe("e2eZip", () => {
  it("builds a zip with one entry per file using the decrypted names", async () => {
    const result = await buildDecryptedZip([
      blob("report.pdf", "aaa"),
      blob("photo.png", "bbb")
    ]);

    const entries = unzipSync(new Uint8Array(await result.arrayBuffer()));
    expect(Object.keys(entries).sort()).toEqual(["photo.png", "report.pdf"]);
    expect(strFromU8(entries["report.pdf"])).toBe("aaa");
    expect(strFromU8(entries["photo.png"])).toBe("bbb");
  });

  it("dedupes colliding names with -2/-3 suffixes", async () => {
    const result = await buildDecryptedZip([
      blob("report.pdf", "one"),
      blob("report.pdf", "two"),
      blob("report.pdf", "three")
    ]);

    const entries = unzipSync(new Uint8Array(await result.arrayBuffer()));
    expect(Object.keys(entries).sort()).toEqual(["report-2.pdf", "report-3.pdf", "report.pdf"]);
    expect(strFromU8(entries["report.pdf"])).toBe("one");
    expect(strFromU8(entries["report-2.pdf"])).toBe("two");
    expect(strFromU8(entries["report-3.pdf"])).toBe("three");
  });

  it("can build an empty zip", async () => {
    const result = await buildDecryptedZip([]);
    const entries = unzipSync(new Uint8Array(await result.arrayBuffer()));
    expect(Object.keys(entries)).toEqual([]);
  });

  it("sanitizes unsafe entry names the same way the server does", async () => {
    const used = new Set<string>();
    expect(uniqueZipName(used, "../escape.txt")).toBe("escape.txt");
    expect(uniqueZipName(used, "a/b.txt")).toBe("b.txt");
    expect(uniqueZipName(used, "..")).toBe("file");
    expect(uniqueZipName(used, "  ")).toBe("file-2");
    expect(uniqueZipName(new Set<string>(), "file")).toBe("file");
  });

  it("produces a safe, lowercase archive filename", () => {
    expect(safeArchiveName("Quarterly Report.pdf")).toBe("quarterly-report");
    expect(safeArchiveName("..--Ideas--..")).toBe("ideas");
    expect(safeArchiveName("Plain")).toBe("plain");
  });
});
