# UltraCompressor II (UC2) archive format

Technical specification for implementers of `github.com/klauspost/uc2`.

Sources:
- the UC2 r2 source in `_reference` (1994, archive version 2.02); `FILE.CPP:n` citations use CRLF line numbers;
- 23 real archives written by r1, r2 and 2.3 (501 file records, 59 masters, every checksum verified);
- the unpacked UC 2.37b binary.

"r1", "r2", "2.3" and "2.37b" are UC2 releases; "r2" also means the published source. MUST and SHOULD apply to writers and readers. Sections marked *informative* describe the original program, not the format.

---

## 1. Overview and conventions

| Item | Rule |
|---|---|
| Integer types | BYTE = u8, WORD = u16, DWORD = u32 (MAIN.H:39-41) |
| Byte order | Little-endian everywhere |
| Struct packing | **Byte-packed.** Structs are written raw with `sizeof`; there is no field serialisation. Proven four ways: the UCD.PRJ word-alignment switch (id 0x00FF) is 0; `sizeof(CONF)` is 357 in three UC.EXE builds; every sample parses with the packed sizes; a non-protected archive has `filesize − dwComponentLength = 26` |
| `char` | Signed (`-K` is off). Matters only for ToKey (§4.5) |
| Offsets | Absolute byte offsets from the start of the file |
| Text | OEM code page of the DOS session that created the archive; not recorded |
| Checksum | 16-bit XOR, called "Fletcher" in the source (§5) |

**Struct sizes (bytes):** FHEAD 13, XHEAD 16, COMPRESS 10, LOCATION 8, OHEAD 1, OSMETA 22, DIRMETA 4, FILEMETA 6, MASMETA 20, EXTMETA 21, XTAIL 17.

**File layout.** Only single-volume archives exist. Multi-volume was never implemented; `.P01` pieces come from the external SAS tool.

```
0          FHEAD (13)
13         XHEAD (16)
29         blobs: headerless compressed streams of files and masters, back to back
           (UC2 writes files, then masters; any order is legal for reading)
locCdir    COMPRESS (10) followed by the compressed raw CDIR stream
L          component end = 13 + FHEAD.dwComponentLength
[L ...]    damage-protection area, only if FHEAD.fDamageProtected (§9)
EOF-13     spare copy of FHEAD (13); always present
[...]      optional UltraSeal appended by an external tool (format unknown)
```

- SUPERMAN.H:6-24 describes the archive as `XHEAD [BAREBLOCKS] CDIR [SEAL] [CRYPT] [BANNER]` inside `FHEAD … [DAMAGE_RECOVERY_INFO]`. It does not mention the spare FHEAD.
- CRYPT is UltraCrypt: the file starts with `UE2` instead of `UC2\x1A`. Its format is unknown.
- BANNER is not a separate block; it consists of ordinary `U$~BAN.*` members (§3.10).

---

## 2. Headers

### 2.1 FHEAD (offset 0, 13 bytes; SUPERMAN.H:64-69)

| Off | Size | Field | Value |
|---|---|---|---|
| 0 | 4 | dwHead | 0x1A324355 = `55 43 32 1A` ("UC2^Z") |
| 4 | 4 | dwComponentLength | L − 13, where L is the file size before the DP area and the spare header |
| 8 | 4 | dwComponentLength2 | dwComponentLength + 0x01B2C3D4 (AMAG), mod 2^32 |
| 12 | 1 | fDamageProtected | 0 or 1 |

- While building an archive, UC2 writes `{magic, 0, AMAG, flag}`, which is a valid placeholder. At close (ARCHIO.CPP:220-270) it writes the final FHEAD at offset 0, then the DP area if flagged, then an identical 13-byte spare at EOF.
- dwComponentLength is used for exactly two things: the DP geometry (`dwPLen = dwComponentLength + 13`, ARCHIO.CPP:125) and the len/len2 check. In a protected archive, a wrong value breaks verification and makes repair truncate real data.

### 2.2 XHEAD (offset 13, 16 bytes; SUPERMAN.H:72-80)

| Off | Abs | Size | Field | Value |
|---|---|---|---|---|
| 0 | 13 | 4 | locCdir.dwVolume | 1 |
| 4 | 17 | 4 | locCdir.dwOffset | Absolute offset of the CDIR's COMPRESS header |
| 8 | 21 | 2 | wFletch | Checksum (§5) of the **raw, uncompressed** CDIR |
| 10 | 23 | 1 | fBusy | 0. Never set to 1 and never checked |
| 11 | 24 | 2 | wVersionMadeBy | §2.4 |
| 13 | 26 | 2 | wVersionNeededToExtract | §2.4 |
| 15 | 28 | 1 | dummy | 0. Never assigned; carried over on update |

- UC2 writes a placeholder XHEAD at create time and the final one after the CDIR.
- r2 `-I`, P and U rewrite XHEAD with fBusy = 0 *before* checking Needed, so never store data in fBusy.
- 2.37b in PCP mode stores a 16-bit value, presumably the PCP checksum, in `dummy` (low byte) and `fBusy` (high byte).

### 2.3 Validation (reader) and physical invariants (writer)

**Reader**, in UC2's order:
1. Bytes 0..3 must be `55 43 32 1A`. `55 45 32` ("UE2") means UltraCrypt and is unsupported (r2: Fatal 125).
2. Check `dwComponentLength2 − dwComponentLength == 0x01B2C3D4` (mod 2^32).
   - If it fails, use the spare. r2 does this only in T; everywhere else the failure is Fatal 200.
   - r2 T also requires the primary and the spare to be byte-identical (Error 90 otherwise).
   - Readers SHOULD locate the spare at L + DP size rather than EOF−13, so that trailing data (an UltraSeal) is tolerated.
3. Check the version (§2.4). r2 does this before reading the CDIR.
4. Check `locCdir.dwVolume == 1`. With any other volume r2 silently skips the seek and decodes garbage.
5. Read the CDIR (§3). If the archive is protected, verify the DP area (§9).

**Writer invariants** (derived from r2's behaviour):

| # | Invariant | Reason |
|---|---|---|
| 1 | Every blob referenced by a file or master lies entirely before locCdir | r2 `-I`, P and U truncate at locCdir and keep the old LOCATIONs |
| 2 | `locCdir.dwVolume = 1` and `dwOffset ≤ file size` | Otherwise every r2 command fails with Fatal 200 |
| 3 | Every `LOCATION.dwVolume = 1` | Otherwise a basic update silently copies the wrong bytes |
| 4 | Nothing follows the spare FHEAD | r2 T reports a header mismatch, and updates under `RELIA=E` are refused |
| 5 | dwComponentLength and len2 are exact | They drive the DP geometry |
| 6 | Every file and master `dwCompressedLength` is exact, in whole 16-bit words | A basic update copies exactly that many bytes (Old2New) |
| 7 | Gaps and unreferenced bytes are allowed | Nothing reads them; a basic update drops them |
| 8 | The CDIR's COMPRESS header is immediately followed by its stream | r2 keeps reading from the same position |
| 9 | Needed ≤ 202 for r2, ≤ 203 for 2.37b | §2.4 |
| 10 | File prefix ≥ 2; master prefix ∈ {0, 1}; master index ≥ 2; master limits of §4.4 | §3.8, §4 |

### 2.4 Version fields

| Value | wVersionMadeBy | wVersionNeededToExtract |
|---|---|---|
| 200 | r1 (2.00) | Default for every UC2 archive |
| 202 | r2 (2.02) | PUC mode (`UC2_PUC=ON`, `.PU2`): masters are compressed with NOMASTER instead of SUPERMASTER. The reader must honour the master's prefix field; r1 always assumed SUPERMASTER |
| 203 | 2.3 and 2.37b | PCP (Private Compression Profile, 2.3+): a user dictionary of up to 58 KB replaces the master. Format not in the source (OPEN) |
| 204 | — | Go port extended mode (§11) |

- r2 `UpdateVersion` (SUPERMAN.CPP:46-60):
  - MadeBy := 202 if it is below 200;
  - Needed := 200 if it is below 200; PUC raises it to 202;
  - values ≥ 200 are kept through updates.
- 2.37b sets MadeBy := 203 if it is below 200. The 2.3 samples carry MadeBy 203 and Needed 200.
- r2 refuses Needed > 202 with Fatal 145, "you need at least UltraCompressor x revision y" (SUPERMAN.CPP:1926-1930). 2.37b refuses Needed > 203. Never write 203 unless the archive is PCP.
- Listings print `MadeBy % 100` as the "revision".
- Go writer: MadeBy 202, Needed 200 (204 in extended mode).
- Go reader: accepts ≤ 202 and 204; 203 is listable but not extractable; > 204 is unsupported.

---

## 3. Central directory (CDIR)

### 3.1 Container

At `locCdir` there is a `COMPRESS{dwCompressedLength, wMethod, dwMasterPrefix}` header, immediately followed by the compressed raw CDIR.

| Field | UC2 writes | Readers |
|---|---|---|
| dwCompressedLength | 0 ("not important") | MUST ignore it in foreign archives. The length is `L − locCdir − 10`. The Go writer stores it exactly (§11) |
| wMethod | `MODE.bCompressor`, forced to 4 if > 9, so never delta. Observed values: 2..5 | Accept 1..9 |
| dwMasterPrefix | 1 (NOMASTER) | Ignore it; always decode with NOMASTER |

- The stream is self-terminating (§6.7). r2 caps the raw output at 100,000,000 bytes; a larger CDIR fails the checksum.
- XHEAD.wFletch covers the complete raw CDIR: records, BO_EOL, XTAIL and serial.
- r2's pipe zero-fills reads past the end, so a raw CDIR that ends early reads as zeros (for example, serial 0).

### 3.2 Raw grammar

```
rawCDIR := record* 04 XTAIL(17) serial(u32)
record  := 01 OSMETA(22) DIRMETA(4)                            [chain]   directory: 27 B + chain
         | 02 OSMETA(22) FILEMETA(6) COMPRESS(10) LOCATION(8)   [chain]   file revision: 47 B + chain
         | 03 MASMETA(20) COMPRESS(10) LOCATION(8)                        master: 39 B
chain   := { EXTMETA(21) data[EXTMETA.size] }    present iff OSMETA.tag != 0;
                                                 another element follows iff EXTMETA.next != 0
```

- The first byte of a record is `OHEAD.bType`: 1 = BO_DIR, 2 = BO_FILE, 3 = BO_MAST, 4 = BO_EOL. Any other value is Fatal 200 in r2, even for L.
- No object count is stored. `CDIRMETA` (SUPERMAN.H:165-168) is never written.
- The SUPERMAN.H comment shows `MASMETA COMPRESS` for masters; the code also writes LOCATION.
- The tag chain follows the record's last fixed struct (DIRMETA or LOCATION), even though its flag sits in OSMETA.
- The empty raw CDIR is `04` followed by 21 zero bytes; its checksum is 0xA55E.

### 3.3 Record structs

**OSMETA (22 bytes):**

| Off | Size | Field | Notes |
|---|---|---|---|
| 0 | 4 | dwParent | Parent directory index; 0 = root |
| 4 | 1 | bAttrib | DOS attribute byte (§3.5) |
| 5 | 2 | wTime | DOS time |
| 7 | 2 | wDate | DOS date |
| 9 | 11 | pbName | FCB 8.3 name (§3.4) |
| 20 | 1 | bHidden | Documented as 0 = visible, 1 = hidden, but never assigned or read. UC2 writes 0xDE (the Vmalloc fill byte), seen in all 501 sample records. Readers ignore it |
| 21 | 1 | tag | Non-zero = an EXTMETA chain follows. UC2 writes 1 exactly when a chain exists |

**Small structs:**

| Struct | Off | Size | Field | Notes |
|---|---|---|---|---|
| DIRMETA | 0 | 4 | dwIndex | Directory index (§3.7) |
| FILEMETA | 0 | 4 | dwLength | Uncompressed size. (The `LOCATION loc` member is commented out; LOCATION is a separate struct) |
| FILEMETA | 4 | 2 | wFletch | Data checksum (§5); delta domain for delta methods |
| COMPRESS | 0 | 4 | dwCompressedLength | Exact stream length in bytes |
| COMPRESS | 4 | 2 | wMethod | §7 |
| COMPRESS | 6 | 4 | dwMasterPrefix | §4 |
| LOCATION | 0 | 4 | dwVolume | 1 |
| LOCATION | 4 | 4 | dwOffset | Absolute offset of the **first byte of the compressed stream**. The header comment "start of object header" is out of date |

**MASMETA (20 bytes):**

| Off | Size | Field | Notes |
|---|---|---|---|
| 0 | 4 | dwIndex | ≥ 2, unique |
| 4 | 4 | dwKey | Grouping key (§4.5). Only writers use it, to reuse masters |
| 8 | 4 | dwRefLen | Sum of dwLength over all file records (all revisions) that use this master. UC2 recomputes it on every write |
| 12 | 4 | dwRefCtr | Number of those records. UC2 drops masters whose count is 0 |
| 16 | 2 | wLength | Raw master length, which is also the dictionary length |
| 18 | 2 | wFletch | Never set or checked. UC2 leaves 0xDEDE |

**EXTMETA (21 bytes):**

| Off | Size | Field | Notes |
|---|---|---|---|
| 0 | 16 | tag | NUL-terminated name, ≤ 15 characters. Bytes after the NUL are undefined (0xDE for tags restored with r2 `~R`); compare only up to the NUL |
| 16 | 4 | size | Payload length. r2 rejects > 1,000,000 as damage; 0 is allowed |
| 20 | 1 | next | 1 if another element follows, else 0. Recomputed on every write |

**XTAIL (17 bytes):**

| Off | Size | Field | Notes |
|---|---|---|---|
| 0 | 1 | bBeta | 0. Never set by r2; preserved |
| 1 | 1 | bLock | 0. Unused by r2; preserved. The source comment lists: 0 none; 1 add with I only, delete allowed; 2 add with I only, no delete; 3 no changes; 128 private |
| 2 | 4 | serial | 0. Unused; preserved |
| 6 | 11 | pbLabel | Volume label (`!VLAB`), FCB-style, space-padded. A first byte of 0 means no label |

**Serial.** The u32 after XTAIL is the creator serial, `CONFIG.dSerial`:
- 0 = unregistered;
- 1 = registered copy running with `UC2_ANONYMOUS`;
- 2212433133 = Pro evaluation;
- anything else = the registration number.

Listings show nothing for 0, "anonymous" for 1, and the number otherwise. Writers SHOULD write 0.

### 3.4 Names

**The pbName field:**
- Bytes 0-7 are the base name and bytes 8-10 the extension. No dot is stored, and both parts are space-padded (0x20).
- UC2 builds it with `Name2Rep(fnsplit(name))` from upper-case DOS short names (DIVERSE.CPP:39-49). Directories use the same form.
- Display form (`Rep2Name`): trim trailing spaces from each part, then show `NAME.EXT`, or `NAME` when there is no extension.
- UC2 compares all 11 bytes exactly; in masks only `?` matches any byte. `A.B.C` is stored as `A.B     C  `.

**Hazards for readers:**
- r2 extracts to `dest + Rep2Name`, so a `\`, `..` or `:` inside a name is a path traversal. Readers MUST sanitize.
- r2 remaps DOS device names only when they start the path (LLIO.CPP:111-124), so `SUB\CON` opens the device.
- The OEM code page is not recorded. unuc2 assumes CP850; the Go reader defaults to CP437 and can be overridden.

**Safe names for writers:**

| Kind | Rule |
|---|---|
| Generated aliases | Characters `A-Z 0-9 _ ~`, plus `-` except in first position. Base 1-8 chars, extension 0-3. No bytes ≥ 0x80. The base is not a DOS device name (CON PRN AUX NUL COM1-9 LPT1-9 CLOCK$ and the list at LLIO.CPP:86-108), whatever the extension. The name does not start with `U$~` |
| Kept verbatim (stored names on copy, caller hints) | Only if unique and safe: no `\ / : * ? " < > \|` and no bytes < 0x20; no `.` or embedded space inside either part; base not empty; not `.` or `..`; first byte not 0x00, 0x05 or 0xE5; base not a device name |
| All names | Unique per directory, across files and directories. One long name maps to exactly one alias, because equal pbName values are treated as revisions of one file |

2.37b never generates aliases. It stores the short name Windows assigned (for example `LONGFI~1`), as returned by DOS findfirst.

### 3.5 Attributes and time

- **bAttrib** is the DOS attribute byte: 0x01 read-only, 0x02 hidden, 0x04 system, 0x10 directory (always set on directory records), 0x20 archive.
- Directories UC2 creates implicitly (`#dest`) have attributes, time and date all 0.
- r2 restores time, date and attributes on extraction for files only.
- **wTime/wDate** hold DOS local wall-clock time with 2-second resolution, for years 1980-2107:

  ```
  wTime = hour<<11 | min<<5 | sec/2
  wDate = (year-1980)<<9 | month<<5 | day
  ```

### 3.6 Tags (EXTMETA chains)

- Only directories and files carry tags; masters cannot.
- Chain order means nothing. r2's `DGet` builds chains by prepending, so every r2 rewrite reverses them. Readers MUST scan the whole chain.
- Listings strip the `AIP:` prefix from tag names.
- r2 bug: with `!DTT`, a skipped file record's tags are not consumed, and the parse loses sync.

**What r2 updates do to tags:**

| Operation | Effect |
|---|---|
| Raw copies (basic update, `-I`, D, P, U) | All tags kept |
| Directory rescanned with S | Loses all its tags |
| New revision from disk | Starts with no tags |
| Basic A of a same-named file | Revision 0 is deleted together with its tags |
| O (optimize, via a tag dump) | File tags ≤ 1000 bytes survive; any directory tag breaks it |

**Known tags:**

| Tag | Written by | Payload |
|---|---|---|
| `AIP:OS/2 2.x EA` (15 chars) | r2, files and directories | Raw OS/2 EA list (INT 21h AX=5702h or EAUTIL). Probably a 16-bit FEALIST (OPEN). r2 applies only the first match |
| `AIP:Version LBL`, `AIP:Comment` | Never; defined only | — (the archive comment is the file `U$~COMM.TXT`) |
| `AIP:Win95 LongN` | 2.37b, files and directories | Long name; see below |
| `UC2X:UTF8Name`, `UC2X:Size64` | Go port | §11 |

**`AIP:Win95 LongN`** (verified in the 2.37b binary):
- **Tag field:** `41 49 50 3A 57 69 6E 39 35 20 4C 6F 6E 67 4E 00`, exactly 16 bytes, so nothing follows the NUL.
- **Payload:** **one path element**, the `cFileName` that INT 21h/714Eh returns for this file or directory.
  - It is in the DOS session's OEM code page. Characters that cannot be mapped become `_` (Windows behaviour). UC2 converts nothing.
- **Size:** 2.37b always writes `size = 270` (0x010E): the name, a NUL, then 0xDE filler up to 270 bytes. Its reader uses `strcpy`, so the NUL is mandatory and `size = strlen+1` is also accepted.
- **When written:** only if `strcmp(fnmerge(fnsplit(shortname)), longname) != 0`. The comparison is case-sensitive, so `readme.txt` next to `README.TXT` gets a tag.
- **Chain order:** 2.37b writes the OS/2 EA element first and LongN second.
- **Extraction:** 2.37b creates the file under its 8.3 name, then renames it with INT 21h/7156h. This happens only if the LFN API is present.
- The tag does not change the version fields.
- **Readers:** the long name is the payload up to the first NUL (the whole payload if there is none), decoded with the OEM charset.

### 3.7 Directories, revisions and canonical order

- **Root.** It is implicit (never written) and has index 0.
- **Directory indices.** UC2 numbers new directories from 1 up and never reuses an index. On read it keeps the stored indices and continues at max+1.
- **Revision order.** Revision 0 is the newest. The CDIR stores a file's revisions **contiguously, oldest first**: rev n−1 … rev 0. Each later record with the same (parent, pbName) becomes the new revision 0.
- **No gaps.** Deleted revisions are simply omitted.
- **Revision identity** is `(dwParent, pbName[11])`.

**Canonical order**, as written by UC2 (SuperDump, SUPERMAN.CPP:1720-1903):

```
emit(D): for each child directory C of D:  BO_DIR(C)
         for each file name F in D:        BO_FILE(F rev n-1) ... BO_FILE(F rev 0)
         for each child directory C of D:  emit(C)
rawCDIR = emit(root) || BO_MAST for every master with dwRefCtr > 0 || 04 || XTAIL || serial
```

### 3.8 r2 parser behaviour and pitfalls (SuperGet, SUPERMAN.CPP:1912-2074)

**Parents first.** A BO_DIR, or a BO_FILE off the fast path, whose parent index is not yet defined gives Error 90 + Fatal 200.

**Parent lookup.** `TCDN` walks the tree depth-first from the root:
- index 0 always means the root, so a BO_DIR carrying index 0 is unreachable;
- duplicate indices resolve to the first match.

**Fast path.** If a BO_FILE's dwParent equals the previous BO_FILE's dwParent, r2 does not navigate. That saved value (`dwPrevDir`) starts at 4100000000, and BO_DIR records do not update it. On the fast path:
- the same pbName as the previous file becomes its newer revision;
- a different name is appended **without a duplicate check**.

Consequences:
- `file(P,A) file(P,B) file(P,A')` produces two independent entries named "A".
- `file(P,A) file(P,B) file(Q,Z) file(P,A') file(P,C)` orphans B: it vanishes from L/V/E/T and the next rewrite drops it.
- `file(P,X) dir(parent Q) file(P,Y)` has no visible effect.
- A directory index of 4100000000 corrupts memory (the fast path uses a stale node).

**Other behaviour:**
- Sibling directories are prepended on read, so each r2 rewrite reverses their order. Files keep their order. Duplicate sibling directory names are accepted without a check.
- A BO_FILE with prefix 0 or 1, or a BO_MAST with dwIndex < 2, corrupts the heap in every r2 command (VNULL dereference, `pbStat[-1]` writes). An internal error only follows after about 55 such records. No real sample contains these.
- A BO_FILE before its BO_MAST is fine. A duplicate master index overwrites the earlier record.
- If a referenced master is missing, E and T fail fatally before extracting anything, and updates hit an internal error.

### 3.9 Writer validation rules

A writer MUST emit, and a validator SHOULD check:
1. BO_DIR indices are unique, with 1 ≤ index < 2^31. Every parent (0 = root) is defined earlier.
2. Each directory's BO_FILE records form one contiguous run. Within it, each (parent, pbName) forms one contiguous run of revisions, oldest first. The canonical order (§3.7) satisfies this.
3. `(parent, pbName)` is unique across files and directories, and every name is safe (§3.4).
4. Every file dwMasterPrefix is ≥ 2 and names an existing BO_MAST. Master indices are unique and ≥ 2, master prefixes are 0 or 1, and §4.4 holds.
5. Tag names are non-empty and NUL-terminated within 16 bytes. Payloads are ≤ 1,000,000 bytes (≤ 1000 to survive r2's O). OSMETA.tag = 1 exactly when a chain follows.
6. The physical invariants of §2.3 hold, and every Ultra stream has an even length.
7. The archive stays within the compatibility budget, or else switches to extended mode (§11):
   - every offset and size ≤ 0x7FFFFFFF;
   - raw CDIR ≤ 100,000,000 bytes;
   - estimated r2 VMEM ≤ ~60 MB;
   - every 8.3 path has a directory part ≤ 63 characters and a full length ≤ 79.

### 3.10 Reserved member names

| pbName | Meaning in UC2 |
|---|---|
| `U$~COMM TXT` (root) | Archive comment, plain OEM text |
| `U$~BAN  GIF`, `JPG`, `TXT`, `MOD`, `ASK` | Banner shown via U2_SHOW.BAT; `.ASK` adds a prompt |
| `U$~NOBASLCK`, `U$~NODELLCK`, `U$~NOADDLCK`, `U$~NOOPTLCK`, `U$~NOUNPLCK`, `U$~NOREVLCK` (any directory) | Forbid basic update, delete, add, optimize, unprotect and comment change, respectively |

L, V, E, T and D skip all `U$~*` members by default, and r2 never adds them from disk.

### 3.11 Limits for compatible archives

| Item | Limit |
|---|---|
| dwLength, dwCompressedLength, offsets, dwComponentLength | 32-bit by format. In practice ≤ 2 GiB − 1 (Borland `long` seeks, FAT16). dwLength also ≤ 0xFFFF8000, above which r2's FlushIt wraps |
| Master wLength | UC2 writes ≤ 62976 and reads with a 63000-byte cap |
| Tag | Name ≤ 15 chars; payload ≤ 1,000,000 bytes |
| Raw CDIR | ≤ 100,000,000 bytes. r2's VMEM holds about 68 MB at most (64512 blocks of 1054 bytes) |
| Paths | r2 path buffers are 120-256 bytes; DOS MAXDIR = 64, MAXPATH = 80 |
| Objects, revisions, tags per object | Unlimited by format |
| Volumes | 1 |

---

## 4. Masters (dictionaries)

### 4.1 Prefix values and use

| dwMasterPrefix | Dictionary (M bytes) |
|---|---|
| 0 = SUPERMASTER | SUPER.DAT, 49152 bytes |
| 1 = NOMASTER | 512 zero bytes (**not** an empty history) |
| ≥ 2 | Decoded content of the BO_MAST with that dwIndex (wLength bytes) |
| 0xDEDEDEDE | Only in a master's own COMPRESS, where r1 left the field unset. Means SUPERMASTER |

Every Ultra stream starts with its dictionary as history: `history = dict ‖ output`. A match at output position p with distance d copies `history[M + p − d]`. Checksums never include the dictionary.

| Stream | Prefix used |
|---|---|
| CDIR | Always NOMASTER |
| File | Its dwMasterPrefix. UC2 writes only values ≥ 2 |
| Custom master | SUPERMASTER in r2 and 2.3; NOMASTER in PUC mode; 0xDEDEDEDE in r1 |
| Supermaster embedded in UC.EXE | NOMASTER, method 4 |

**Chained masters** (a master whose own prefix is ≥ 2) are not implemented in UC2; the dictionary comes out as garbage (NEUROMAN.CPP:481, "QQQ other masters too ???"). Never write them; readers reject them.

### 4.2 SUPER.DAT, the supermaster

- **Identity.** 49,152 bytes (96 × 512).
  - SHA-256: `4f2e3fb48a288f66a76b0e26cb2830f3f0297f3686752853de65b82e646e840e`.
  - Checksum: **0x1E55**. UC2 checks this after decoding the dictionary from UC.EXE and otherwise fails with Fatal 105 "UC.EXE is damaged".
- **Provenance.**
  - Proven byte-identical to the dictionary embedded in r1 UC.EXE, UE.EXE and r2 UC.EXE. There it is stored as a method-4 NOMASTER stream at `CONF.dwSoffset`: 22,972 bytes in r1 and UE, 22,940 bytes in r2.
  - The 2.3 and 2.37b executables could not be unpacked, but every 2.3 sample decodes with SUPER.DAT.
- **When it is needed.** For almost every `.UC2` archive, because custom masters are supermaster-compressed. Not for `.PU2` archives, and never for the CDIR.
- **Other `.DAT` files.** `SUP04.DAT` (checksum 0x512F) is an unused pre-release supermaster. `TSE.DAT` and `QSLIST.DAT` are unrelated to the format.

### 4.3 Master records and decoding

- A BO_MAST record is MASMETA + COMPRESS + LOCATION. The blob at LOCATION is a headerless stream of dwCompressedLength bytes that decodes to the master content.
- Decode it with the master's own method and prefix (0xDEDEDEDE → 0). UC2 caps the output at 63000 bytes.
- A master stored with a delta method is un-delta'd after decoding (verified on the BLDRIP sample, method 43). The supermaster used as its prefix is never delta-transformed.
- Master content has no checksum (MASMETA.wFletch is unused). Damage shows up only in the dependent files.

### 4.4 Custom master constraints (safe range)

| Constraint | Reason |
|---|---|
| `wLength % 512 == 0` | r2's encoder removes hash entries only at 512-aligned ring blocks. If r2 reuses a misaligned master for new files, it silently emits wrong matches (d ∈ 63489..64000) |
| `512 ≤ wLength ≤ 62976` | 0 breaks r2's cache and delta code. More than 63000 exceeds the read cap. The encoder ring needs ≤ 63488. 62976 (123 × 512) is UC2's own limit |
| Stream decodes to exactly wLength bytes | UC2 hands out wLength bytes from its cache; short or long output leaves stale or foreign bytes |
| `dwMasterPrefix ∈ {0, 1}` | Chained masters are broken |
| `wMethod ∈ 1-9, 21-37, 40-47`; 80 only if the output is exactly wLength bytes | 38, 39, 48 and 49 overflow the delta state; other values fail |
| dwCompressedLength exact | Masters are copied raw on basic update |
| dwKey: any value | A key UC2 never generates stops r2 from reusing the master. Example: 0 (plain keys are ≥ 0x7F7F80, even with signed-char sign extension) |

**Reachability.** A writer limited to d ≤ 64000 can reach only the last `64000 − p` dictionary bytes at output position p. The whole supermaster is reachable only for the first 14,848 output bytes.

### 4.5 ToKey (NEUROMAN.CPP:61-116)

**Input.** The upper-case 8.3 name, split by `fnsplit` into `name[9]` and `ext[5]` (ext includes the dot). Both arrays are zero-filled first.

**Signed `char`.** Bytes ≥ 0x80 sign-extend when cast to DWORD; in Go, `uint32(int32(int8(b)))`. Borland's `isdigit` on such bytes is undefined, so generated aliases stay ASCII.

```
plain key (default; "file type bundling"):
  name[i] = '#' if digit (i = 0..7);  ext[i] = '#' if digit (i = 1..3)
  if ext[0] != 0:  pad ext with ' ' until strlen >= 3          (".C" -> ".C ", ext[3] stays 0)
                   key = 0x01000000 + ext[1]<<16 + ext[2]<<8 + ext[3]
  else:            pad name with ' ' until strlen >= 8
                   key = 0x02000000 + name[0]<<16 + name[1]<<8 + name[2]
hyper key (ToHKey; per name; no '#', no padding; DWORD arithmetic, '+' may carry):
  r  = 0x07000000 + n0<<16 + n1<<8 + n2
  r ^= n3<<16 + n4<<8 + n5
  r ^= e1<<17 + e2<<10 + e3<<2
  r ^= n6<<16 + n7<<9
  r ^= n0*13 ^ n1*317 ^ n2*46513 ^ n3*9361 ^ n4*3 ^ n5*17513 ^ n6*32513 ^ n7*7517
  r ^= e1*129 ^ e2*64327 ^ e3*3541                  (nX = name[X], eX = ext[X])
```

| Name | Key |
|---|---|
| `*.C` | 0x01432000 |
| `*.H` | 0x01482000 |
| `*.CPP` | 0x01435050 |
| `*.001` | 0x01232323 |
| `MAKEFILE` | 0x024D414B |

For ASCII names the top byte of a hyper key is 6 or 7. Readers ignore keys.

### 4.6 UC2's master policy (*informative*)

**Assignment** (ScanAdd, SUPERMAN.CPP:1042-1221):
- Look for an existing master whose key equals `ToHKey(name)`. If there is none, find or create the master for `ToKey(name)`.
- New masters take index `newindex++`. The counter starts at 2 and is raised past every index read from the archive.
- Every new file gets a prefix ≥ 2.
- An existing archive master with a matching key is reused unchanged, not rebuilt.

**Quota** (TuneNeuro; the per-file limit equals the total): 32768 bytes for methods 2 and 22; 62976 for 3, 23, 30-39, 4, 24, 5, 25 and 80.

**Size classes:** 0 is < 2000 bytes, 1 is 2000..9999, 2 is ≥ 10000. Each class is a LIFO chain.

**Building a master:**
1. Walk classes 1, 0, 2.
2. Each file contributes its first `min(quota, size)` bytes.
3. A file that fits whole (`n == size > 0`) becomes "special", and its offset in the master is recorded (§8.6).
4. Zero-pad the result to `max(512, roundup(len, 512))`.

**Compressing masters:**
- Method: the archive mode. Under TT/TST it is the Analyze result of the master's first file, which may be a delta method (30..33 or 40..43).
- Prefix: SUPERMASTER, or NOMASTER in PUC mode.
- Master blobs are written after all files and before the CDIR. BO_MAST records follow all directories and files.
- RefLen and RefCtr are recomputed on every write. Masters with RefCtr = 0 are dropped.

**File compression order:** for each master in creation-LIFO order, its files in classes 1, 2, 0; then the SUPERMASTER chains; then the NOMASTER chains.

**Other details:**
- UC2 2.3 builds a 512-zero custom master for an empty file.
- Optimize uses "hypermode": files with several revisions are re-added one revision at a time, each name getting its own master.
- The Vmalloc fill byte 0xDE explains MASMETA.wFletch = 0xDEDE, bHidden = 0xDE and r1's master prefix 0xDEDEDEDE.

---

## 5. Checksum ("Fletcher")

```
fletch(data) = 0xA55A XOR ( XOR over k of LE16(data[2k], data[2k+1]) )
```

- A trailing odd byte is a low byte with high byte 0.
- The result does not depend on how the stream is split into chunks: FLETCH.CPP carries a pending-odd-byte flag between calls.
- The 386 path (XOR of dwords, folded to 16 bits) gives the same result.
- OFLETCH.CPP, a real Fletcher, is not linked and returns immediately.

| Input | Checksum |
|---|---|
| (empty) | 0xA55A |
| `a` | 0xA53B |
| `ab` | 0xC73B |
| `abc` | 0xC758 |
| `abcd` | 0xA358 |
| `123456789` | 0xAD63 |
| `A` | 0xA51B |
| `5A A5` | 0x0000 |
| 512 × `00`, or 512 × `FF` | 0xA55A |
| `04` + 21 × `00` | 0xA55E |
| SUPER.DAT | 0x1E55 |

| Field | Covers |
|---|---|
| FILEMETA.wFletch | File data as seen by the LZ stage (delta rule below); never the dictionary |
| XHEAD.wFletch | The complete raw CDIR |
| DP check table | Each 512-byte sector, plus one checksum of the table itself (§9) |
| Supermaster | Must be 0x1E55 |
| MASMETA.wFletch | Never computed or checked |

**Delta-domain rule.** For delta methods (21-29, 30-37, 40-47), wFletch covers the **delta-transformed** bytes, not the file bytes: the encoder applies the delta before checksumming, and the decoder checksums its ring output before undoing the delta. Methods 1-9 and 80 checksum the plain file bytes. Output is truncated at dwLength; there is no separate length check.

**Weakness.** XOR is linear, so these go undetected: reordered words, two flips in the same bit column, and a sector read back as all zeros whose stored checksum was 0xA55A (typical for pad sectors).

---

## 6. Ultra bitstream (methods 1-9, 21-29, 30-37, 40-47)

### 6.1 Bit I/O

- **Word order.** The stream is a sequence of 16-bit little-endian words, read MSB-first. Stream bit k is bit `15 − (k mod 16)` of word `⌊k/16⌋`. So the first 8 stream bits are byte 1, MSB first, then byte 0. A byte-wise MSB-first reader is wrong.
- **Fields.** Codes, extra bits and header fields are all written MSB-first (`PUTBITS(v, n)`). Unlike DEFLATE, extra bits are not reversed.
- **Flush.** The last partial word is padded with zero bits. The compressed size is always even, and `dwCompressedLength` counts whole words.
- **Over-read.** r2 reads 1024-byte chunks and so reads past the stream. Readers MUST treat bits past the end as zeros and MUST ignore everything after the terminating `0` bit.

### 6.2 Stream grammar

```
Stream := Block* '0' zero-pad-to-16-bit-word
Block  := '1' Trees Item* EOB
Trees  := '0'                                        default trees; history := default lengths
        | '1' t:2 pre:15x3 PreCoded(Max entries)      explicit trees, delta-coded vs history (6.6)
Item   := LD(0..255)                                 literal byte
        | LD(256..315) distExtra L(0..27) lenExtra   match
EOB    := LD(315) extra12 = 2561   L(one coded symbol, NO extra bits)
```

- No block length, block count or checksum.
- The tree history (344 lengths) starts as the default lengths at the start of each stream and persists across blocks.
- Empty input is the single bit `0`, i.e. the bytes `00 00`.

### 6.3 Alphabets

- **LD:** 316 symbols. 0..255 are literals; 256..315 are distance slots.
- **L:** 28 length symbols. In the combined 344-entry length array, LD is 0..315 and L is 316..343.
- **Pre-tree:** 15 symbols. 0..13 are delta codes; 14 is the repeat marker.

**Distances.** Decoded distance = base + extra; extra bits are raw, MSB first. The slot for d = 64001 (symbol 315, extra 2561) is the EOB marker.

| LD symbols | Distance range | Base | Extra bits |
|---|---|---|---|
| 256..270 | 1..15 | s − 255 | 0 |
| 271..285 | 16..255 | (s − 270)·16 | 4 |
| 286..300 | 256..4095 | (s − 285)·256 | 8 |
| 301..315 | 4096..65535 | (s − 300)·4096 | 12 |

**Lengths:**

| L symbol | Bases | Extra bits | Range |
|---|---|---|---|
| 0..7 | 3, 4, …, 10 | 0 | 3..10 |
| 8..15 | 11, 13, 15, 17, 19, 21, 23, 25 | 1 | 11..26 |
| 16..23 | 27, 35, 43, 51, 59, 67, 75, 83 | 3 | 27..90 |
| 24 | 91 | 6 | 91..154 |
| 25 | 155 | 9 | 155..666 |
| 26 | 667 | 11 | 667..2714 |
| 27 | 2715 | 15 | 2715..35482 |

**Encoder mapping** (`sym / extra value / extra bits`):

```
d < 16:   255+d / - / 0        d < 256:  270+(d>>4) / d&15 / 4
d < 4096: 285+(d>>8) / d&255 / 8                else: 300+(d>>12) / d&4095 / 12
n < 11:   n-3 / - / 0          n < 27:   8+(n-11)/2 / (n-11)&1 / 1
n < 91:   16+(n-27)/8 / (n-27)&7 / 3            n < 155: 24 / n-91 / 6
n < 667:  25 / n-155 / 9       n < 2715: 26 / n-667 / 11           else: 27 / n-2715 / 15
```

### 6.4 Huffman codes

- **Canonical codes, as in DEFLATE.** Symbols with non-zero length are sorted by (length, symbol) and get consecutive codes; the counter shifts left when the length increases (CodeGen, TREEGEN.CPP:440-483).
- **Lengths.** LD and L codes are at most 13 bits. Pre-tree codes are at most 7 bits (they are stored in 3-bit fields). Length 0 means unused.
- **r2 decoding.** An 8192-entry table indexed by the next 13 bits. Each symbol of length ℓ fills the next 2^(13−ℓ) entries in canonical order (DCodeGen).
- **Incomplete codes are legal.** The default LD tree itself sums to 1016/1024. A lookup that hits an unfilled entry is corruption (r2 would use stale memory).
- **Oversubscribed codes MUST be rejected.** r2 would write past its tables. Writers never produce them.
- **UC2 degenerate cases.** TreeGen gives a single used symbol s the lengths {s: 1, (s+1) mod n: 1}, and gives {0: 1, 1: 1} when nothing is used. Since EOB always counts L0, a literal-only block has L lengths {L0: 1, L1: 1}.

### 6.5 Default ("base") trees (BasePrev, TREEENC.CPP:55-70)

**Assignment**, in order; later steps overwrite earlier ones:
- LD: 0..31 ← 9; then 10, 12, 32 ← 7; 33..127 ← 8; then 46 `.`, 58 `:`, 92 `\` ← 7; 128..255 ← 10; 256..271 ← 6; 272..283 ← 7; 284..289 ← 8; 290..299 ← 9; 300..315 ← 10.
- L: L0..L8 ← 4; L9..L17 ← 5; L18..L27 ← 6.

| Length | Symbols in canonical order | Codes (decimal) |
|---|---|---|
| 6 | 256..271 | 0..15 |
| 7 | 10, 12, 32, 46, 58, 92, 272..283 | 32..49 |
| 8 | 33..45, 47..57, 59..91, 93..127, 284..289 | 100..197 |
| 9 | 0..9, 11, 13..31, 290..299 | 396..435 |
| 10 | 128..255, 300..315 | 872..1015. Codes 1016..1023 are unused, so peeks starting `1111111` are invalid |
| L 4 / 5 / 6 | L0..L8 / L9..L17 / L18..L27 | 0..8 / 18..26 / 54..63 (the L code is complete) |

**Sample codes:**
- LD 0 `110001100`, LD 4 `110010000`, LD 65 'A' `10000010`, LD 256 `000000`, LD 271 `001111`, LD 273 `0100111`, LD 315 `1111110111`.
- L0 `0000`, L13 `10110`, L27 `111111`.
- Default EOB: `1111110111 101000000001 0000` (26 bits).

### 6.6 Explicit tree transmission (TreeDec, TREEENC.CPP:277-358)

1. Read bit `1` (explicit trees).
2. Read `t`, 2 bits, MSB first. The first bit is **0x02** (lengths of 128..255 present), the second **0x01** (lengths of control characters present).
3. Read 15 × 3-bit pre-tree lengths, for pre-symbols 0..14.
4. Decode pre-coded entries until there are `Max = 344 − (t&1 ? 0 : 28) − (t&2 ? 0 : 128)` of them:
   - a symbol s in 0..13 sets `val = s` and appends s;
   - symbol 14 reads one more pre-symbol c (0..14, not treated as a marker) and appends `c + 5` copies of `val`;
   - `val` starts at 0 in every tree header;
   - truncate to Max. A repeat can overshoot, and r2 corrupts its heap when that happens with t = 3.
5. Map entries to symbols in this order:
   - if t&1, symbols 0..31; otherwise only 9, 10, 12, 13 (not 11);
   - then 32..127;
   - if t&2, 128..255;
   - then 256..343.
6. Set `len[s] = vval[prev[s]][entry]`; symbols not sent get length 0. Then set `prev = len` for all 344 symbols, zeros included.

**Encoder (TreeEnc):**
- Each entry is `table[prev[s]][len[s]]`.
- t&1 is set iff any of 0..31 other than 9, 10, 12, 13 is non-zero; t&2 iff any of 128..255 is.
- RLE runs over the whole entry sequence, across group boundaries:
  - a run of n > 6 equal entries is capped at 20 and written as `v, 14, n−6`, and the scan resumes after those n entries;
  - shorter runs are written verbatim.
- The pre-tree is built from the post-RLE frequencies, with a maximum length of 7.

**Delta matrices** (InitTables, TREEENC.CPP:90-133; regenerated, and checked that `vval[p][table[p][n]] == n`):

```
table[prev][new]  (encoder)               vval[prev][code]  (decoder)
p\n  0  1  2  3  4  5  6  7  8  9 10 11 12 13     p\c  0  1  2  3  4  5  6  7  8  9 10 11 12 13
 0   0 13 12 11 10  9  8  7  6  5  4  3  2  1      0   0 13 12 11 10  9  8  7  6  5  4  3  2  1
 1  13  0  1  2  3  4  5  6  7  8  9 10 11 12      1   1  2  3  4  5  6  7  8  9 10 11 12 13  0
 2  13  1  0  2  3  4  5  6  7  8  9 10 11 12      2   2  1  3  4  5  6  7  8  9 10 11 12 13  0
 3  13  3  1  0  2  4  5  6  7  8  9 10 11 12      3   3  2  4  1  5  6  7  8  9 10 11 12 13  0
 4  13  5  3  1  0  2  4  6  7  8  9 10 11 12      4   4  3  5  2  6  1  7  8  9 10 11 12 13  0
 5  13  7  5  3  1  0  2  4  6  8  9 10 11 12      5   5  4  6  3  7  2  8  1  9 10 11 12 13  0
 6  13  9  7  5  3  1  0  2  4  6  8 10 11 12      6   6  5  7  4  8  3  9  2 10  1 11 12 13  0
 7  13 11  9  7  5  3  1  0  2  4  6  8 10 12      7   7  6  8  5  9  4 10  3 11  2 12  1 13  0
 8  12 13 11  9  7  5  3  1  0  2  4  6  8 10      8   8  7  9  6 10  5 11  4 12  3 13  2  0  1
 9  10 13 12 11  9  7  5  3  1  0  2  4  6  8      9   9  8 10  7 11  6 12  5 13  4  0  3  2  1
10   8 13 12 11 10  9  7  5  3  1  0  2  4  6     10  10  9 11  8 12  7 13  6  0  5  4  3  2  1
11   6 13 12 11 10  9  8  7  5  3  1  0  2  4     11  11 10 12  9 13  8  0  7  6  5  4  3  2  1
12   4 13 12 11 10  9  8  7  6  5  3  1  0  2     12  12 11 13 10  0  9  8  7  6  5  4  3  2  1
13   2 13 12 11 10  9  8  7  6  5  4  3  1  0     13  13 12  0 11 10  9  8  7  6  5  4  3  2  1
```

### 6.7 EOB and termination

- **EOB.** LD 315 with the 12 extra bits 2561 (distance 64001 = 125·512 + 1), then exactly one L code, whose extra bits are **not** read.
  - `nuke1` never looks at the value of that L symbol, but the symbol MUST have a code in the current L tree.
  - Use L0. UC2 does, and any symbol < 8 also keeps the beta decoder `nuke2` in sync.
  - LD 315 MUST have a code. 64001 is the only reserved distance.
- **The final `0` bit is required.** r2 stops early only when the output *exceeds* its cap, so a stream that produces exactly dwLength bytes can only end through the `0`.
- **Where readers stop:** files at dwLength; masters at wLength (UC2's cap is 63000); the CDIR at the `0` bit (r2's cap is 100,000,000).
- Readers do not validate what follows the last needed byte: UC2's SpComp can leave garbage after the last useful match (§8.6).

### 6.8 Window and history

- **r2 decoder.** A 64 KiB ring. The dictionary sits at [0, M) and output starts at index M. Matches are forward byte copies, so overlapping copies replicate; indices wrap mod 65536.
- **Valid distances:** `1 ≤ d ≤ 65535`, `d ≠ 64001` and `d ≤ M + pos`, where pos is the number of bytes output so far. A larger d reads ring memory r2 never wrote, so readers reject it.
- **Valid lengths.** r2's 32 KiB half-flush imposes `L + ((M + pos) mod 32768) ≤ 65535`.
  - L ≤ 32768 is always safe.
  - UC2's encoder uses ≤ 32760 (≤ 30000 in SpComp).
  - Readers decode up to 35482.

### 6.9 Decoder constraints checklist

| # | Writer MUST | Reader SHOULD |
|---|---|---|
| 1 | Use `1 ≤ d ≤ M+pos` and `d ≠ 64001`; default to d ≤ 64000 (r2 decodes up to 65535, but the 2.3/2.37b decoders cannot be inspected) | Reject d > M+pos; accept up to 65535 |
| 2 | Use 3 ≤ L ≤ 32768 (exact rule in §6.8) | Decode lengths up to 35482 |
| 3 | End each block with LD 315 + 2561, then one coded L symbol < 8 (L0), with no extra bits | Read and discard exactly one L code |
| 4 | Keep code lengths ≤ 13 (pre-tree ≤ 7) and never oversubscribe. Every emitted symbol, including LD 315 and the EOB L symbol, has a code | Reject oversubscribed codes; accept incomplete ones; treat unfilled lookups as corruption |
| 5 | Default-tree blocks may appear anywhere; afterwards the history is the default lengths | Same |
| 6 | Any number of items and blocks | Impose no limits |
| 7 | Final `0` bit, zero padding to a 16-bit word, exact even dwCompressedLength | Ignore bits after the `0`; pad reads with zeros |
| 8 | Methods 1..9, 21..29, 30..37, 40..47 (and 80) only | Reject 0, 10..20, 38, 39, 48..79 and ≥ 81 |
| 9 | Pre-tree repeats never run past Max; set the t bits for any non-zero out-of-set length | Truncate an overshoot to Max |

### 6.10 Golden vectors

| Input | Dictionary | Method | Stream |
|---|---|---|---|
| (empty) | any | any Ultra | `00 00` |
| `A` (checksum 0xA51B) | NOMASTER | any Ultra | `BF A0 01 7A 00 00` |
| `04` + 21 × `00` (empty raw CDIR), as written by UC.EXE | NOMASTER | 3 | `09 B2 CF D2 80 DE 00 40` (literal 04, match d=52 L=21) |
| Same input, stale-memory variant (decode test only) | NOMASTER | 3 | `07 B2 9F B5 00 BD 00 80` (match d=22) |
| 512 × `00` (2.3 empty-file master) | SUPERMASTER | 3 | `EA BE D8 38 EC 01 F7 EB 10 A0 00 00` |
| SUPER.DAT | NOMASTER | 4 | r2 UC.EXE bytes [110466, 133406): 22,940 bytes (external) |

**Bit layouts:**
- `A`: `1 0 10000010 1111110111 101000000001 0000 0` (37 bits). The words are 0xA0BF, 0x7A01 and 0x0000.
- Empty CDIR: `1 0 110010000 0100111 0100 10110 0 1111110111 101000000001 0000 0`.

UC.EXE's empty-CDIR output is deterministic (d=52), because its CDIR pipe zero-fills the input buffer.

**60-byte empty archive** (r2, method 3, unregistered):

```
00  55 43 32 1A 22 00 00 00 F6 C3 B2 01 00              FHEAD: len 0x22 = 60-26, len2 = len+0x01B2C3D4, no DP
0D  01 00 00 00 1D 00 00 00 5E A5 00 CA 00 C8 00 00     XHEAD: locCdir {1,29}, fletch 0xA55E, busy 0, 202, 200, 0
1D  00 00 00 00 03 00 01 00 00 00                       COMPRESS {0, 3, NOMASTER}
27  09 B2 CF D2 80 DE 00 40                             CDIR stream
2F  55 43 32 1A 22 00 00 00 F6 C3 B2 01 00              spare FHEAD
```

---

## 7. Methods, delta filter, turbo

### 7.1 Method table (COMPRESS.wMethod; COMPINT.CPP:102-187, 227-259)

| wMethod | Decoder | Encoder in UC2 / origin |
|---|---|---|
| 0 | Invalid (there is no store method) | Internal error |
| 1 | Ultra | TuneComp(5,2,5,25); internal and Analyze trials only |
| 2 / 3 / 4 / 5 | Ultra | `-TF` / `-TN` (default) / `-TT` / `-TST` |
| 6..9 | Ultra | Never written |
| 10..20 | Invalid | — |
| 21..29 | Ultra + delta size 1 | Level m−20; 24 = `-TM` |
| 30..37 | Ultra + delta size m−29 (1..8) | Level 4; `-TM1..8`; Analyze under `-TT` gives 30..33 |
| 38, 39 | Invalid: delta size 9/10 overflows `arra[8]` | — |
| 40..47 | Ultra + delta size m−39 (1..8) | Level 5; Analyze under `-TST` gives 40..43 |
| 48, 49 | Invalid (same overflow) | — |
| 50..69 | Invalid | Analyze trial runs only; never stored |
| 70..79, ≥ 81 | Invalid | — |
| 80 | Turbo | `-TSF`. The encoder exists only in beta builds; the release has the decoder |

- The level affects only the encoder. All Ultra methods share one bitstream format.
- If r2 meets an unknown **file** method:
  - L is unaffected;
  - T reports Error 90 for that file;
  - E stops with Fatal 200 when it reaches the file and leaves a partial output file behind;
  - a basic update copies the stream raw.
- An unknown **master** method makes E fail fatally before anything is extracted.

### 7.2 Delta filter (DELTA.CPP, COMPINT.CPP)

- **State:** `size` (1..8), `ctr = 0`, `arra[8] = {0}`, fresh for every stream.
- **Forward:** `out = in − arra[ctr]; arra[ctr] = in; ctr = (ctr+1) % size`.
- **Inverse:** `out = in + arra[ctr]; arra[ctr] = out; ctr = (ctr+1) % size`. All arithmetic is mod 256.
- **Encoder:** the whole input is delta'd before LZ, and the checksum is taken on the delta'd bytes (§5).
- **Decoder:** the LZ window holds delta-domain bytes, which are checksummed; the output then goes through the inverse delta.
- **Dictionary:** for a delta method whose prefix is not SUPERMASTER, the dictionary is forward-delta'd with a fresh state before use, in both the encoder and the decoder. NOMASTER's zeros stay zeros, and SUPERMASTER is never transformed.
- The CDIR never uses delta.

### 7.3 Turbo, method 80 (COMP_TT.CPP)

**Prediction table.** `T[32768]` starts zeroed and is seeded from the dictionary: `for i in 0 .. M−11: T[(m[i]<<7) ^ m[i+1]] = m[i+2]`. If M ≤ 10 there is no seeding.

**Stream layout:**

```
Stream := { u16le count (1..28125)  Group* }  u16le 0
Group  := control(1)  up to 8 items; bit (7-j) of control set = item j is predicted,
          clear = one literal byte follows (in item order)
```

- The encoder cuts the input into blocks of at most 25000 raw bytes.
- **Prediction** = `T[(pp<<7) ^ p]`, where p and pp are the previous two output bytes. Both start at 0 and persist across blocks. A literal sets `T[h] = byte`.
- **Decoding.** Within a block, the decoder keeps processing items while block bytes remain *or* the shifted control byte is still non-zero.
- The checksum covers the plain output.
- r2's turbo decoder ignores the output cap, so the stream MUST produce exactly dwLength (or wLength) bytes.
- No 2.3/2.37b turbo decoder has been verified. The Go writer re-encodes turbo files instead of copying them raw.

---

## 8. The original encoder (*informative*)

UC2's encoder output is not part of the format. This section serves ratio tuning and explains the sample archives.

### 8.1 Level parameters

`TuneComp(maxDepth, lazyDepth, lazyLimit, giveUp)` per method:

| Method | maxDepth | lazyDepth | lazyLimit | giveUp |
|---|---|---|---|---|
| 1 | 5 | 2 | 5 | 25 |
| 2 | 15 | 2 | 15 | 25 |
| 3 | 70 | 10 | 30 | 50 |
| 4 | 600 | 50 | 40 | 100 |
| 5 | 10000 | 5000 | 200 | 100 |
| 5 with env `TUX=ON` | 60000 | 60000 | 500 | 500 |

Constants: MAX_LEN = 200 (longest match found directly), MAX_XLEN = 32760, MAX_DIST = 64000, READ_SIZE = 512.

### 8.2 Match finder (ULTRACMP.CPP)

- **Ring.** R = 64512 bytes plus a 512-byte mirror of ring[0..512). The dictionary occupies ring[R−M, R), directly before data position 0.
- **Hash.** `HASH(p) = b[p] ^ b[p+1]<<3 ^ (b[p+2]&0x7F)<<6`: 13 bits, 8192 heads.
- **Chains.** Newest-first, with a live count per head. Removal is count-based, one 512-byte block at a time.
- **Dictionary insertion.** Positions −M..−10 are inserted before any data is read; −9..−1 after the first 1024 data bytes.
- **FindMaxLen(pos, depth, off):**
  - examines `min(count, depth)` candidates;
  - quick-filters on the byte at offset `off`, which is 2 normally and the current length for the lazy probe;
  - caps `matchLen` at 200;
  - a strictly longer match wins, so ties go to the newest candidate;
  - stops once the best length exceeds giveUp;
  - always inserts pos.

### 8.3 Parse loop

1. Take the best match at `cur`. If d > 64000, emit a literal; there is no fallback to a shorter candidate.
2. If `len < lazyLimit`, probe `cur+1` with lazyDepth. If the probe finds a longer match, emit a literal and take that match next.
3. Insert every covered position.
4. If `len ≥ 200` and `d < 63488`, extend the match 200 bytes at a time, up to 32760.
5. Input: any short read ends it. At EOF, the final match is truncated; a remainder under 3 bytes is emitted as literals.

### 8.4 Blocks and trees

**Buffering and blocks:**
- Items are buffered as words: a literal takes 1 word, a match 2.
- There are 28 sub-buffers of 490 words. A block is flushed when the 28th sub-buffer overflows, at ≤ 13,749 words including EOB. The environment variable `UC2_HUFBUF` sets the sub-buffer count to value/490, clamped to 5..47.
- Block boundaries are purely count-based.
- Default trees are used only if `sub == 0` and the block has fewer than 256 words including EOB, i.e. for a tiny final block. UC2 never emits empty blocks.

**TreeGen:**
- It is a heap Huffman over u16 frequencies with exact tie rules; Reheap takes the right child when `f[l] ≥ f[r]`.
- Its result then goes through the `RepairLengths` limiter (13 bits, or 7 for the pre-tree).

**Hazard.** RepairLengths line 290, `while (!LengthCount[--j]);`, can read `LengthCount[-1]` for LD trees. That is undefined behaviour.
- A reachable example: freq[0..254] = 49, freq[255] = 34, freq[256..302] = 1, freq[315] = 1.
- A faithful port needs a fallback here. The Go port uses an optimal length-limited algorithm instead.

### 8.5 Analyze: automatic delta selection for -TT/-TST (COMPINT.CPP:295-484)

1. Analyze runs only in mode 4 or 5, and only for files larger than 1500 bytes. It samples 1450 bytes starting at offset `(size−1450)/2`.
2. **Baseline:** build a byte histogram with `fq[0] = 0`, run `TreeGen(…,316,13)`, compute `cost = Σ f·len`, and set score = `cost·100/1450`.
3. **Candidates**, for n = 1..4: delta-n the sample, drop the bytes at i ≥ 5 where the deltas at i, i−1 and i−2 are all zero, and score the result with TreeGen over 256 symbols.
4. A candidate wins if `score < best − 5`. This is a DWORD comparison, so it wraps when best < 5.
5. A winner gives method 29+n (mode 4) or 39+n (mode 5). A trial run with a counting writer and NOMASTER then confirms it:
   - mode 4: method 1 against 49+n, reading 512 of every 2048 bytes from file offset 1;
   - mode 5: method 2 against 59+n, over the whole file.

   The delta method is kept only if its trial output is smaller.

### 8.6 SpComp and reproducibility

**SpComp** handles a file that lies entirely inside its master:
- The stream is made of `min(rem, 30000)`-byte matches at distance `wLength − offset`, with default trees.
- The method is the archive mode if it is ≤ 10, otherwise 3. The shortcut is disabled under TT/TST while the master's method is still unset.
- **Bug.** Files of 1-2 bytes, or with sizes ≡ 1 or 2 mod 30000, produce a length below 3. It is written as an LD code where an L code belongs; r2 reads it as L27 plus 15 extra bits, copies correctly thanks to the output cap, and leaves garbage behind.
- Decoders stop at dwLength (§6.7). Writers never emit a length < 3.

**Reproducibility.** Matching near EOF reads up to 200 bytes past N, and those bytes are stale ring memory. Bit-exact reproduction of UC.EXE output is therefore impossible in general. It is possible for the empty CDIR (the pipe input is zero-filled) and for inputs whose last LZ-domain byte occurs nowhere earlier in the input or the dictionary.

---

## 9. Damage protection (DAMPRO.CPP)

### 9.1 Geometry and layout

```
L    = dwComponentLength + 13            file size before protection (the component end)
secs = floor(L / 512) + 1                always +1: the pad is 1..512 bytes, never 0
pad  = secs*512 - L
drs  = 1 if secs < 200, 2 if < 400, 4 if < 800, 8 if < 1600, else 16
```

| Offset | Size | Content |
|---|---|---|
| 0 | L | FHEAD + component. The final FHEAD (flag set) is written before protection, so sector 0 covers it |
| L | pad | Pad. UC2 **never writes byte L** (`Seek(L+1)`, then zeros), so it holds stale disk content. Bytes L+1 .. secs·512−1 are 0 |
| secs·512 | drs·512 | Parity record x = XOR of all sectors i < secs with i mod drs = x |
| (secs+drs)·512 | 2·secs | Check table: LE16 checksum (§5) of each 512-byte sector i = 0..secs−1 |
| + 2·secs | 2 | LE16 checksum of the check-table bytes |
| + 2 | 13 | Spare FHEAD |

- **Total file size:** `514·secs + 512·drs + 15`.
- The DP area has no magic number. It is found only through fDamageProtected and dwComponentLength.
- **Gap byte.** Real archives contain 0xD6 and 0xCD at byte L. The stored checksums and parity include that byte, because UC2 reads the file back to compute them.
  - Writers put 0x00 at L and compute over what they wrote.
  - Verifiers MUST use the actual byte and never normalise it.

### 9.2 Generation, verification, repair

**Generation:**

```
pad the file to secs*512 (byte L = 0)
for i in 0..secs-1: s = sector i; par[i % drs] ^= s; chk[i] = fletch(s)
append par[0..drs); append chk[] as LE16; append LE16 fletch(chk bytes); append spare FHEAD
```

**Verification (VerifyDP):**
1. A bad table checksum is Error 90, "fatal damage in damage protection records". Nothing more can be verified.
2. Check every sector against the table. A mismatch is "sector N is damaged" (N is 1-based).
3. Recompute the parity. A mismatch while all sectors are OK is "protection record x is damaged (but all data is 100% OK)".

Parity records have no checksum of their own.

**Repair (RepairDP; output FIX_nnnn.UC2):**
1. If the table checksum is bad, copy all sectors verbatim; the repair fails.
2. Otherwise copy each good sector. For a bad sector i, compute `t = parity[i mod drs]` XOR every other sector `j ≡ i (mod drs)`, and accept t if `fletch(t) == chk[i]`.
3. Truncate the output to L, re-protect it (UC2 always does, even after a failure), and append the spare header.

**Repair limits:**
- At most one bad sector per class `i mod drs`, and that class's parity record must be intact. Equivalently, bursts of up to drs sectors are repairable: 8 KiB when secs ≥ 1600.
- Overhead is about 1% near 1600 sectors; beyond that it is 8 KiB plus 2 bytes per sector.
- UC2's DWORD arithmetic limits DP to archives below 4 GiB.

### 9.3 Verified numbers

| Case | L | secs | drs | pad | DP bytes (pad + 512·drs + 2·secs + 2) | Other |
|---|---|---|---|---|---|---|
| chaos100.uc2 (r2) | 4561 | 9 | 1 | 47 | 579 | File 5153; table checksum E23E; byte L = 0xD6 |
| WARLORDS.UC2 | 316793 | 619 | 4 | 135 | 3423 | File 320229; table checksum D16B; byte L = 0xCD |

**Synthetic vectors.** Each data byte is `(x>>16) & 0xFF`, with `x = x·1103515245 + 12345` (mod 2^32) starting from x = 1. Pad bytes are 0.

| L | secs | drs | pad | chk[0..] | Table checksum | par0[0:8] | Size without spare |
|---|---|---|---|---|---|---|---|
| 1000 | 2 | 1 | 24 | A8C8 B479 | B9EB | 5f6dd0b9718c4fc6 | 1542 |
| 1024 | 3 | 1 | 512 | A8C8 1B3E A55A | B3F6 | 5f6dd0b9718c4fc6 | 2056 |
| 204800 | 401 | 4 | 512 | A8C8 1B3E 72CA … A55A | AB38 | 4687852c32dd41d1 | 208164 |

---

## 10. Updates (*informative*, UC2 behaviour)

**Create.** Write a temp file `U$~xxxxx.TMP`: FHEAD placeholder, XHEAD placeholder, files, masters, CDIR. Then write the final XHEAD and FHEAD, the DP area and the spare header, and rename the temp file to the archive name.

**Basic update** (A without I, D, R, and removing empty directories):
1. Read the old archive; write a new temp file.
2. Write the new files first. A non-incremental A deletes, i.e. replaces, the newest old revision of each same-named file.
3. Copy the surviving old streams raw (Old2New, `dwCompressedLength` bytes) and rewrite their LOCATIONs.
4. Compress the new masters; copy the used old ones raw.
5. Write the CDIR and rename. `!BAK` keeps a backup.

**Incremental update** (`-I`, P, U, and ensure-mode updates):
1. Open the archive in place and rewrite XHEAD with fBusy = 0.
2. Parse the CDIR.
3. Write `ARCH.UR2`.
4. Truncate at the old locCdir, which removes the old CDIR, the DP area, the spare header and any seal.
5. Append the new blobs. Old files and masters keep their LOCATIONs.
6. Write the CDIR, XHEAD, FHEAD, DP area and spare header, then delete the `.UR2`.

**`.UR2` crash-recovery file:**

| Off | Size | Content |
|---|---|---|
| 0 | 4 | `UR2!` |
| 4 | 29 | Original FHEAD + XHEAD |
| 33 | 4 | `from` = old locCdir.dwOffset |
| 37 | 4 | `len` = old file size − from |
| 41 | len | Old archive tail from `from` |

- Restoring writes back the 29 header bytes, calls `SetFileSize(from+1)` and writes the tail at `from`.
- All commands refuse to run while a `.UR2` exists; T restores it. UC2 also restores it automatically after an abnormal exit.

**Locks.**
- An external `ARCH.ULC` file blocks every command unless `!NOLOCK` is given.
- In-archive lock markers are listed in §3.10.
- XTAIL.bLock is unused.

The invariants 1 and 6 of §2.3 are what make r2 updates of foreign archives work.

---

## 11. Extensions used by this Go port

- **Compatibility.** Only extended mode (Needed = 204) changes compatibility. Everything else stays readable and r2-updatable by r2, 2.3 and 2.37b.
- **Tag order.** For new entries the writer emits LongN, then UTF8Name, then foreign tags, then Size64. Copied entries and in-place updates keep the source's name and foreign tags verbatim and in source order (duplicates included); only Size64 is regenerated. Readers MUST scan the whole chain; order means nothing.
- **Tags are not part of the public API.** Readers use the first LongN/UTF8Name they find and ignore empty ones and ones longer than 64 KiB.

### 11.1 `AIP:Win95 LongN` as written by the Go port

```
41 49 50 3A 57 69 6E 39 35 20 4C 6F 6E 67 4E 00   tag "AIP:Win95 LongN\0"
SS 00 00 00                                       size = strlen(name) + 1
nn                                                next
<long name element in the OEM charset> 00          payload: name + NUL, no filler
```

- **Size differs from 2.37b.** 2.37b writes a fixed size of 270 with 0xDE filler. Its reader uses `strcpy`, so `strlen+1` reads fine, and the smaller size cuts the per-entry cost in r2's VMEM by about 3×.
- **When written:** only if the long name differs from the alias's `NAME.EXT` form, compared case-sensitively (2.37b's `strcmp` rule), and its charset form is at most 259 bytes (2.37b copies it into a MAX_PATH-sized buffer). Directories get the tag too.
- **Aliases** follow the Win95 FAT basis-name algorithm: upper-case, spaces and leading dots removed, `+ , ; = [ ]` and unmappable characters become `_`, the base is the part before the first dot (at most 8 bytes), the extension the first 3 bytes after the last dot, and `~N` is appended when anything was lost. UC 2.37b hangs restoring a long name if the file system generates a different 8.3 name than the stored one, so the alias should match what the file system would generate.
- **Character mapping:** characters the charset cannot map, and characters Windows forbids (< 0x20 and `" * : < > ? \ |`), become `_`.

### 11.2 `UC2X:UTF8Name`

```
55 43 32 58 3A 55 54 46 38 4E 61 6D 65 00 00 00   tag "UC2X:UTF8Name\0" (zero-filled to 16)
LL LL LL LL                                       size = byte length of the UTF-8 element (1..65536)
nn                                                next
<raw UTF-8 bytes of the name element, no NUL>
```

- **When written:** only for names the caller supplied as UTF-8 (Create, CreateHeader, AddFS, FileInfoHeader) whose element is non-ASCII, had to be sanitized for the LongN form, or is too long for LongN. Tags over 1000 bytes break r2's `O` command, which is harmless for reading.
- **Copy** carries the tag only if the source entry already had one. It is never synthesised from a foreign name decoded with a guessed charset.
- **Compatibility:** r2 preserves unknown tags on raw copies.
- **Reader name resolution**, per path element:
  1. UTF8Name, if it is valid UTF-8;
  2. otherwise LongN (up to the NUL, charset-decoded);
  3. otherwise the 8.3 name (trimmed `NAME.EXT`, charset-decoded).

  After that, `/`, `\`, control characters (C0, DEL, C1), and any empty, `.` or `..` element, become `_`.

### 11.3 Extended mode (Needed = 204)

**Triggers** (any one switches the archive to extended mode):
- A file or master offset, locCdir, dwLength or compressed length exceeds 0x7FFFFFFF. So does the archive end: the component end plus the DP area plus the 13-byte spare. This also covers r2's 0xFFFF8000 FlushIt limit.
- The raw CDIR exceeds 100,000,000 bytes (r2's decode cap).
- Estimated r2 VMEM use exceeds ~60 MB, estimated as raw CDIR + ~100 B per file record + 18 B per name node + (35 + 8 + size) B per tag.
- An entry's 8.3 path (aliases joined by `\`) has a directory part over 63 characters (MAXDIR) or a full length over 79 (MAXPATH). Longer paths overflow r2's fixed path buffers.

**Encoding:**

```
XHEAD.wVersionNeededToExtract = CC 00                           (204)
every LOCATION, including XHEAD.locCdir:
    dwVolume = 1 + (off >> 32), dwOffset = uint32(off)
CDIR COMPRESS.dwCompressedLength = exact compressed CDIR length  (must be < 2^32)
component end L = locCdir + 10 + CDIR dwCompressedLength         (64-bit)
FHEAD.dwComponentLength  = uint32(L - 13)
FHEAD.dwComponentLength2 = uint32(dwComponentLength + 0x01B2C3D4)
UC2X:Size64 on every file whose dwLength or compressed length is >= 2^32:
    55 43 32 58 3A 53 69 7A 65 36 34 00 00 00 00 00   tag "UC2X:Size64\0" (zero-filled to 16)
    10 00 00 00                                       size 16
    nn                                                next
    <u64 LE uncompressed size> <u64 LE compressed size>
    (FILEMETA.dwLength and COMPRESS.dwCompressedLength hold the low 32 bits)
damage protection: the §9 algorithm with 64-bit L, secs and offsets
```

- **What changes.** If the only triggers are CDIR size, VMEM or path length, the CDIR bytes are identical to compatible mode: all volumes are 1 below 4 GiB and no Size64 tag is needed. Only Needed changes.
- **Readers:**
  - compute the component end from the 64-bit locCdir + 10 + the CDIR dwCompressedLength;
  - require `uint32(L − 13) == dwComponentLength` and `len2 == dwComponentLength + 0x01B2C3D4`;
  - treat a Size64 tag, or `dwVolume ≠ 1`, in an archive with Needed < 204 as a format error.
- **DOS UC2:**
  - r2 and 2.37b stop with error 145 before reading the CDIR. The file stays byte-identical: r2 `-I/P/U` rewrite only byte 23 (fBusy), with the 0 already there.
  - On a protected extended archive, T may run the DP test first on the low 32 bits and report 90 instead of 145. The original file is still unmodified.

### 11.4 Compatible writer conventions (not format changes)

**Header and filler fields:**
- CDIR `COMPRESS.dwCompressedLength` is exact (UC2 writes 0 and ignores it).
- MadeBy is 202; Needed is 200, or 204 in extended mode.
- bHidden, MASMETA.wFletch, the creator serial, XTAIL bBeta and XTAIL bLock are all 0.
- Tag-name bytes after the NUL are 0, and so is the DP gap byte at L.

**Bitstream:**
- Only forms UC2's own encoder produces: `d ≤ 64000`, `3 ≤ L ≤ 32760`, EOB followed by L0, code lengths ≤ 13 (pre-tree ≤ 7), no oversubscription, no RLE overshoot.
- The writer **never emits empty blocks or mid-stream default-tree blocks**. Default trees appear only in a stream's final block, as in UC2. This reproduces the `A` golden vector.

**Masters:**
- Every file references a custom master (prefix ≥ 2).
- Generated masters use prefix 0, a method from 2..5, and satisfy §4.4 (`wLength % 512 == 0`, 512..62976).
- Files that use no dictionary share one archive-wide 512-zero master with key 0. These are delta-coded files and files without master material, such as empty files.
- A master blob may sit between file blobs (written after its batch). All blobs precede locCdir.

**Raw Copy:**
- Files with methods 1-9, 21-37 or 40-47 are copied raw. Method-80 files are re-encoded.
- A copied master must have prefix 0 or 1 (0xDEDEDEDE is rewritten to 0), a method from 1-9, 21-37 or 40-47, and satisfy the §4.4 length rules.

**Output bytes.** The compressed bytes differ from UC.EXE's. Byte-exact reproduction is not a goal (§8.6).
