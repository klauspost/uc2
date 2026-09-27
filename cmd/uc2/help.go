package main

func (a *app) help() {
	a.outf(`UltraCompressor II (Go port), reads and writes UC2 revision 2 archives

SYNTAX: uc2 command[options] [options] archive[.UC2] [files...] [& command ...]

COMMANDS: A M F D E   add / move / freshen / delete / extract (also X)
              L V     list / verbose list (all revisions)
              P U     damage protect / unprotect
                T     test (& repair into FIX_nnnn.UC2)
                O     optimize (recompress, TT by default)
                R     revise archive comment

OPTIONS: (directly after command, or preceded by '-' or, on Windows, '/')
      TF TN TT TST    fast / normal / tight / super tight compression
                 S    include subdirectories
                 M    move mode (delete files after adding / extracting)
                 F    force mode (never ask, always overwrite)
               I B    incremental mode (keep versions) / basic mode
               P U    add / remove damage protection while writing
            !NEWER    only files newer than their counterpart

  ;n specify version   ;* all versions   !exclude files   #destination
  ##[dest] destination + source path     & concat commands   @script

LONG FORMS: --recurse (-r) --move --force (-f) --incremental (-i) --basic
  --protect --unprotect --newer --level=fast|normal|tight|super --dest=DIR (-d)
  --exclude=PAT (-x) --rev=N|all --charset=437|850 --threads=N
  --comment-file=FILE --verbose (-v) --quiet (-q) --help (-h) --version
  Commands: add move freshen delete extract list verbose test protect
  unprotect optimize comment. Everything after -- is a file name.
  Put -- before shell wildcards: uc2 a arch -- *

Not supported: C (convert), $ and ~ commands, !DTT, !CONTAINS, !QUERY,
  lock files, !VLAB, !RELIA, banners.
`)
}
