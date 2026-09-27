# uc2 — UltraCompressor II for Go

A modern Go implementation of the [UltraCompressor II](https://en.wikipedia.org/wiki/UltraCompressor_II)
archive format (`.UC2`), the 1992–1996 DOS archiver by Nico de Vries / AIP‑NL.

Ported by Claude Code Opus 5.5.

* Reads archives made by every UC2 release (revision 1, 2, 2.3, 2.37b): all compression
  methods (including multimedia/delta and turbo), custom masters, file revisions, long file
  names, damage protection.
* Writes archives that the original DOS UC2 reads and updates, unless the input requires one of
  the [documented extensions](#extensions).
* API modelled on `archive/zip`, plus `io/fs` support.
* Parallel compression with deterministic output, streaming decompression, bounded memory.
* `cmd/uc2`: a command line tool with the original's syntax.

```
go get github.com/klauspost/uc2
```
This is created as a celebration of UC2, 
which was one of the programs that peaked my interest in compression.
Consider this a toy project for fun and research only.

While it has been reviewed and tested against the original UC2 
and additional modern safety measures have been implemented,
it is not a production-quality library. 

Use at your own risk.

## Usage

### Reading

```go
r, err := uc2.OpenReader("ARCHIVE.UC2")
if err != nil {
	return err
}
defer r.Close()
for _, f := range r.File {
	fmt.Println(f.Name, f.Size, f.Revision)
	rc, err := f.Open() // checksum is verified at EOF
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, rc)
	rc.Close()
	if err != nil {
		return err
	}
}
```

`Reader` implements `fs.FS`, showing the newest revision of each file:

```go
data, err := fs.ReadFile(r, "src/main.c")
err = os.CopyFS("out", r)
```

### Writing

```go
f, _ := os.Create("NEW.UC2")
w := uc2.NewWriter(f, uc2.WithLevel(uc2.Tight))
fw, _ := w.Create("docs/Read me.txt")
io.WriteString(fw, "hello")
w.AddFS(os.DirFS("src")) // or add a whole tree
if err := w.Close(); err != nil { ... }
f.Close()
```

`NewWriter` needs an `io.WriteSeeker` that allows overwriting (not a file opened for appending):
the header, which points at the central directory, is written last. `AddFS` reads files ahead
concurrently, so the `fs.FS` must be safe for concurrent use. Options:

| Option | Meaning |
|---|---|
| `WithLevel(l)` | `Fast`, `Normal` (default), `Tight`, `SuperTight` — UC2's `-TF/-TN/-TT/-TST`. Tight and SuperTight detect multimedia data and apply UC2's delta filters. |
| `WithConcurrency(n)` | Compression goroutines (default `GOMAXPROCS`). The output is identical for any value. |
| `WithDamageProtection(on)` | Add UC2 damage protection (≈1% overhead) so damaged sectors can be repaired. |
| `WithCharset(cs)` | Code page for DOS names and long names; `CP437` (default) or `CP850`, or any `golang.org/x/text/encoding/charmap` code page. |

### Updating

Like `archive/zip`, archives are updated by copying: `Writer.Copy` copies entries, keeping their
8.3 names and tags, without recompressing them when possible. The source archive is read until
`Writer.Close`, so keep it open and unchanged until then. To recompress an entry while keeping its
names and tags, use `w.CreateHeader(&f.FileHeader)` and copy `f.Open()` into it.

```go
w := uc2.NewWriter(tmp)
for _, f := range r.File {
	if f.Name != "old.txt" {
		w.Copy(f)
	}
}
```

`NewAppendWriter` updates an archive in place, like UC2's incremental mode (`A -I`): existing data
stays untouched, new files and new revisions are appended, and the header rewrite at `Close` is the
single commit point. Closing an append writer without adding anything, with
`WithDamageProtection(true/false)`, adds or removes damage protection (UC2's `P` and `U`). Until
`Close`, the file is locked against other append writers (`flock` on Unix, `LockFileEx` on
Windows); DOS UC2 does not honour the lock.

### Concepts

* **Revisions.** UC2 keeps multiple versions of a file. `Reader.File` lists all of them, oldest
  first; `File.Revision` is 0 for the newest. Adding an existing name to a `Writer` adds a new
  newest revision.
* **Names.** UC2 stores DOS 8.3 names (`File.ShortName`) and, since 2.37b, the long name in a tag.
  The writer generates unique 8.3 aliases (`VERYLO~1.C`) with the Win95 algorithm and stores long
  names the way UC 2.37b does, so DOS UC2 extracts the 8.3 names and UC 2.37b restores the long
  ones. `FileHeader.ShortName` can suggest an alias.
* **Times** are DOS local time with 2 second resolution, 1980–2107 (values outside are clamped).
* **Masters.** UC2 compresses files against shared dictionaries ("masters") built from files of
  the same type, plus a built-in 48 KiB dictionary. This gives solid-archive compression with
  random access. The writer does this automatically.
* **Comment and label.** `SetComment`/`Reader.Comment` use UC2's `U$~COMM.TXT` root file;
  `SetLabel`/`Reader.Label` the volume label field. Other `U$~*` files (banners, locks) are
  hidden from `fs.FS`.
* **Checking.** `Reader.Check` verifies the archive structure and damage protection without
  decompressing; `File.Open` verifies each file's checksum.

## Performance

Compressing the C++ sources of UC2 itself (602,609 bytes), one core, compared with Go's
`compress/flate`:

| | Size | Compress | Decompress |
|---|---|---|---|
| uc2 Fast | 24.11% | 118 MB/s | 488 MB/s |
| uc2 Normal | 23.17% | 70 MB/s | 511 MB/s |
| uc2 Tight | 22.85% | 33 MB/s | 542 MB/s |
| uc2 SuperTight | 22.72% | 9 MB/s | 558 MB/s |
| flate -1 | 30.18% | 185 MB/s | 330 MB/s |
| flate -6 | 24.00% | 60 MB/s | 422 MB/s |
| flate -9 | 23.90% | 21 MB/s | 426 MB/s |

The format's 64,000-byte window, matches up to 32,760 bytes and cheap delta-coded Huffman tree
headers compress better than deflate at every level. In archives, masters shared by files of the
same type add more: the complete UC2 source tree (177 files, 1,092,846 bytes) becomes 269,566
bytes at Normal, against 353,235 bytes for a zip archive at deflate level 6.

The writer compresses files, and fragments of large files, on all cores; the output is identical
for any concurrency setting.

## Compatibility

Archives written by this package are read, tested and updated by the original UC2 revision 2,
2.3 and 2.37b. The writer only emits structures the original emits itself (see
[docs/format.md](docs/format.md) for the invariants), and this is verified by running the real
UC2 in DOSBox-X (see [Testing](#testing)):

* `UC T` accepts archives written at every level, with and without damage protection, including
  long, Unicode and device names;
* `UC E` extracts them byte for byte (UC 2.37b restores the long names, see the known issue
  below);
* UC2 updates them: adding files (reusing our masters), incremental adds, deleting, protecting
  and unprotecting, after which this package reads the result and can update it again.

The compressed bytes differ from what `UC.EXE` produces; the original's output cannot be
reproduced exactly (it depends on uninitialized memory).

An archive automatically switches to **extended mode** when DOS UC2 could not handle it:

* any file, offset or the archive itself larger than 2 GiB − 1 (DOS UC2's practical limit),
* a central directory larger than 100,000,000 bytes or too large for UC2's memory manager
  (roughly 100,000+ files),
* an 8.3 path deeper than DOS allows (63 characters for the directory, 79 in total).

Extended archives set "version needed to extract" to 204, so every DOS UC2 refuses them cleanly
("you need at least UltraCompressor 2 revision 4") instead of misbehaving.

**Known UC 2.37b issue.** When UC 2.37b restores a long name, it hangs if the file system gives the
file a different 8.3 name than the one stored in the archive. The writer generates aliases the way
Windows 95 does to avoid this, but it can still happen when several long names share a prefix
(the `~N` number depends on the extraction order and on existing files) and, under DOSBox-X, for
non-ASCII names. It also happens with archives UC 2.37b made itself. UC2 revision 2 and 2.3, which
extract the 8.3 names, are not affected.

## Extensions

These are the only additions to the format. Readers that don't know them ignore them (tags) or
refuse the archive (extended mode).

### `UC2X:UTF8Name` tag

Written for a file or directory whose name contains non-ASCII characters, or characters that can
not be stored in the `AIP:Win95 LongN` tag. It holds the name element in UTF-8, so names round
trip exactly regardless of code page. The `AIP:Win95 LongN` tag (with unmappable characters as
`_`) is written as well, for UC 2.37b.

```
55 43 32 58 3A 55 54 46 38 4E 61 6D 65 00 00 00   tag name "UC2X:UTF8Name"
LL LL LL LL                                       size, little-endian
nn                                                1 if another tag follows
<UTF-8 bytes of the name element, no terminator>
```

Readers prefer `UC2X:UTF8Name`, then `AIP:Win95 LongN`, then the 8.3 name.

### Extended mode (archives needing 64-bit values)

* `XHEAD.wVersionNeededToExtract` = 204.
* Every `LOCATION` (including `XHEAD.locCdir`) stores `dwVolume = 1 + (offset >> 32)`,
  `dwOffset = offset & 0xFFFFFFFF`. (Standard archives always use volume 1.)
* Files with a size or compressed size ≥ 2³² carry a `UC2X:Size64` tag; the 32-bit fields hold
  the low halves:

  ```
  55 43 32 58 3A 53 69 7A 65 36 34 00 00 00 00 00   tag name "UC2X:Size64"
  10 00 00 00  nn                                   size 16, next
  <u64 size> <u64 compressed size>                  little-endian
  ```

* `FHEAD.dwComponentLength` holds the low 32 bits; the component ends at
  `locCdir + 10 + dwCompressedLength` of the central directory's `COMPRESS` record.
* Damage protection uses the same algorithm with 64-bit sizes.

### Writer conventions (compatible)

* The central directory's `COMPRESS.dwCompressedLength` is exact (UC2 writes 0 and ignores it).
* `AIP:Win95 LongN` tags store `strlen + 1` bytes (UC 2.37b writes a fixed 270-byte buffer) and
  are only written for names of at most 259 bytes in the code page, which is what UC 2.37b can
  hold; longer names are kept in `UC2X:UTF8Name` only.
* Unset fields UC2 fills with memory garbage (`0xDE`) are written as zero.
* Blocks of nearly incompressible data grow up to 54,880 words, 4 times UC2's default and 2.4
  times its maximum (`UC2_HUFBUF`); UC2's decoder has no block size limit. This cuts the overhead
  on incompressible data from 0.21% to 0.08%.
* Master blobs are written after the files of each batch rather than all at the end; files without
  a useful dictionary share one 512-byte zero master (UC2 itself requires every file to reference
  a custom master).

## Limits

* DOS UC2 reads archives up to 2 GiB; this package handles 64-bit sizes via extended mode.
* Names: path elements up to 64 KiB of UTF-8, with no limit on the path length; `.`/`..`, `\` and
  NUL are rejected. At most 4 Mi entries per directory.
* Names read from archives are sanitized, so they never escape the extraction directory:
  separators, control characters, `.`/`..` and trailing dots or spaces become `_`. Names that
  reach a `.git` directory through a file system alias (`GIT~1`, `.git::$INDEX_ALLOCATION`,
  ignorable Unicode on macOS) get a `_` prefix; `.git` itself is kept, so be careful when
  extracting untrusted archives with `os.CopyFS` into a repository.
* Reading bounds memory use by hostile archives: the central directory may expand at most
  128-fold (256 MiB in total), and entries and paths may use at most 256 bytes per compressed
  byte. Overlapping or truncated compressed streams are rejected.
* `AddFS` only adds regular files and directories, and fails if a file is replaced or changes
  size while it is added.
* Not supported: UltraCrypt-encrypted archives (`UE2`), archives using UC 2.3 private compression
  profiles (they can be listed, but not extracted), verification of UltraSeal signatures,
  multi-volume (SAS) sets.
* Damage protection keeps 2 bytes per 512-byte sector in memory while writing.

## Command line

`cmd/uc2` implements the original commands with the original syntax. Install it with
`go install github.com/klauspost/uc2/cmd/uc2@latest`, or download a binary for Linux, Windows,
macOS or the BSDs from the GitHub releases. Examples:

```
uc2 a archive *.txt           add files
uc2 astt archive src\*.*      add recursively (S) with tight compression (TT)
uc2 ai archive file.doc       add as a new revision (incremental)
uc2 l archive                 list          uc2 v archive     verbose list with revisions
uc2 es archive #out\          extract with subdirectories into out\
uc2 t archive                 test (and repair into FIX_nnnn.UC2)
uc2 p archive                 add damage protection
```

Also `m` (move), `f` (freshen), `d` (delete), `u` (unprotect), `o` (optimize) and `r` (comment),
the option letters `TF TN TT TST S M F I B P U`, `;n`/`;*` revision selection, `!exclude`,
`#dest`, `##`, `@script` and `&`. GNU-style long flags are accepted as well
(`uc2 add --recurse --level=tight archive dir`), though not in script files; run `uc2 -?` for a
summary.

On a terminal of at least 80×20 characters, `uc2` without arguments opens UC2's
full-screen help menu. It shows chapters 0-8 of the UltraCompressor II 2.4 manual, with the
license chapter replaced by this port's, in UC2's viewer: `0`-`8` and `A`-`Z` jump to a chapter
or paragraph, `S` searches all of them, `Tab` prints the chapter's summary and exits, and `Esc`
goes back. As with UC2, `uc2 -? words` opens the viewer on a search, and `uc2 -? 105` explains
error 105. Without a terminal, and for `uc2 -?` or `uc2 --help`, the short help is printed instead.

Masks match either the DOS 8.3 name with DOS wildcard rules or the long name with a
case-insensitive glob. Arguments that start with `@`, `&`, `-`, `#` or `!` are taken as file names
if such a file exists, so a shell wildcard can't inject commands; still, prefer `uc2 a arch -- *`.
Likewise, a whole line of a script file that names an existing file is that name.
Symbolic links that point outside the added tree are skipped, and files that change while being
added or moved are skipped, not deleted.

Extraction never writes outside the target directory and, on Windows, maps reserved names (`CON`,
`a:b`, ...) to safe ones. It refuses aliases of `.git` (`GIT~1`, `.git.`) and never writes into an
existing `.git` directory. Commands that rewrite an archive (`A`, `M`, `D`, `R`, `O`, `EM`) refuse
damaged archives; repair them with `T` first. Updates keep the archive's permissions, and are
aborted if another process changes the archive meanwhile.

The output looks like UC2's: its logo, texts and colors, and, on a terminal, revision 2's
`······` → `■■■■■■` progress bars. Colors are used on terminals only; `--color=never|always`
overrides that, and `NO_COLOR` or `TERM=dumb` switch them off. `UC2_NO_HIGH_ASCII` draws the logo
and bars in ASCII, as it did in UC2. Unlike UC2, errors, warnings and the error summary go to
stderr. Questions appear when stderr is a terminal and, as in UC2, take a single key from the
console; `+` aborts. Without a terminal, existing files are skipped with a warning.

Exit codes follow UC2's error levels (0 OK, 20 nothing matched, 90 damage found, 145 newer version
needed, ...). Not supported: `C` (convert), the `$` commands, the configuration menu,
`!DTT`, `!CONTAINS`, `!QUERY`, lock files, `!VLAB`, `!RELIA` and banners.

UC2's commands for front ends work as well. They show no logo and, instead of "Everything went
OK", create `U$~RESLT.OK` in the current directory when they succeed (unless `UC2_OK=OFF`) and
delete it after errors and warnings:

* `uc2 ~D archive [masks]` prints a fixed-format recursive listing, with UC 2.37b's long names
  and UC2's CRLF line ends.
* `uc2 ~X archive dumpfile` writes the tags of all entries to a binary dump, and
  `uc2 ~R archive dumpfile` applies such a dump to the archive in place. Both take the archive
  name as given. Unlike UC2, `~R` checks the whole dump first and changes nothing if it is
  invalid, and it applies directory entries too. Long name and size tags are managed by the port
  and stay as they are, with a warning if the dump changes them.
* `uc2 ~K path` deletes everything below `path` except names that contain `.U~K`. Symbolic links
  and junctions are removed, never followed.
* `uc2 ~V file` shows a text file in the help viewer.
* `~M` and `~~` are accepted and do nothing.

This makes `uc2` a drop-in replacement for `UC.EXE` in Total Commander (Configuration → Packer):
listing, extracting, adding and deleting work. Total Commander shows the 8.3 names, as it did
with UC2. Names in its list files may contain spaces or start with `-`; when adding, they may
also start with `@`, `&`, `#` or `!`. Total Commander copies the names from `~D` unquoted into
its scripts, so `~D` shows a leading `@`, `&`, `#` or `!` as the wildcard `?`: a hostile archive
cannot inject commands, and the file can still be extracted.

## Testing

```
go run testdata/fetch.go   # download third-party sample archives (SHA-256 pinned)
go test ./...
```

Tests cover golden vectors derived from the original source, all public sample archives, fuzzing
(`go test -fuzz=FuzzReader`, `FuzzDecode`), a strict validator that checks every stream the writer
produces against the forms UC2's encoder emits, crash-injection for in-place updates, and
sparse-file tests for extended mode (`UC2_BIGTESTS=1` also writes a real 4 GiB file).

The oracle tests run the real UC2 (revision 2, 2.3 PRO and 2.37b) in DOSBox-X to cross-check both
directions:

```
UC2_DOSBOX=/path/to/dosbox-x.zip go test -run Oracle -v .
```

The binaries are downloaded and cached outside the repository. If your UC2 PRO copy needs
registering, pass the code in `UC2_REGCODE`; it is never written to the repository.

## Format documentation

[docs/format.md](docs/format.md) is a complete, verified description of the UC2 format, derived
from the original source code and checked against real archives.

The format seems to be quite zip/deflate-like. Based on AR002 (the LZ77+Huffman design behind LHA) according to the manual.

No major divergences, but extensions to various functionality...

* Window: deflate 32KB -> UC2 64KB match distance (64,000 used by the encoder)

* Shared dictionaries. Allows savings across files of the same type, and allows random access to each file. ZIP is per-file. Deflate supports preset dictionaries (zlib), but zip never uses them.

* Longer matches. Deflate's longest match is 258 bytes, UC2's is 32,760 bytes. Small gain.

* Alphabet split is inverted. Deflate merges literals with match lengths (286 symbols) and codes distances separately. UC2 merges literals with 60 distance slots (316 symbols) and codes lengths separately (28 symbols). So the "literal or match" decision is coded together with the distance.

* Different bucketing. UC2 has 15 direct codes for distances 1–15. Beyond that each range uses 15 uniform slots with 4, 8 or 12 raw bits, where deflate uses a log scale. Lengths 3–10 are direct, then the buckets widen fast, up to 32,760.

* Cheap tree headers. Each block's code lengths are delta-coded against the previous block's tree, run-length coded, and can omit whole ranges (control characters, bytes ≥ 128). Deflate sends every dynamic tree from scratch. The built-in default tree favours printable ASCII.

* Codes are limited to 13 bits (deflate: 15). That's slightly less efficient on very skewed data, but it allows a single 8K-entry decode table, which is part of why decompression is fast.

* End-of-block is a special distance (64001) rather than a dedicated symbol.

* No stored blocks/methods. Incompressible data costs about 0.2–0.4% in literal-only blocks; deflate's stored blocks cost about 0.01%. Strange choice.

* Optional delta filters for audio and images.

* Central directory is compressed.

* Damage protection: interleaved XOR parity sectors plus per-sector checksums (~1%) allow repairing damaged sectors.

* File revisions are supported. UC2 can store multiple versions of a file in the same archive, and the reader exposes them all.

## Security

This repo will not accept security reports.

You are however welcome to send pull requests with fixes.

## License and provenance

UltraCompressor II was written by Nico de Vries (AIP-NL), who released its source code under the
GNU LGPL v3 in 2015. This package is a new implementation derived from that source, and embeds
UC2's built-in dictionary (`internal/super/super.dat`, taken verbatim from the release).
It is therefore licensed under the LGPL v3 (see `LICENSE` and `COPYING`).

The help menu of `cmd/uc2` embeds chapters 0-8 of the UltraCompressor II 2.4 manual
(`cmd/uc2/manual.txt`), with its license chapter replaced by this port's LGPL notice. The manual
is (c) Ad Infinitum Programs, included under the 2015 LGPL source release; explicit permission
has been requested from Nico de Vries.

The terminal code in `cmd/uc2/internal/term` is derived from `golang.org/x/term`, (c) The Go
Authors, under its BSD license (`cmd/uc2/internal/term/LICENSE`, in release archives
`third_party/golang.org/x/term/LICENSE`).
