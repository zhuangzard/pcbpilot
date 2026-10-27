// Package kfile is the single write path for KiCad project files (.kicad_pcb,
// .kicad_sch, .kicad_pro, .kicad_sym, …) and the reports pcbpilot writes
// next to them.
//
// Guarantees:
//
//   - Atomic replace. WriteFile and Create write a sibling temp file
//     (".<base>.pcbpilot-*.tmp"), fsync it, copy the target's mode and rename it
//     over the target (Windows: MoveFileExW REPLACE_EXISTING|WRITE_THROUGH;
//     Unix: rename then fsync of the directory). A reader sees the old bytes or
//     the new bytes, never a mix; on any error the temp file is removed and the
//     target is untouched.
//   - Sharing violations retried. On Windows a rename that hits
//     ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION or ERROR_ACCESS_DENIED (a
//     viewer, antivirus or a sync client holding the file) is retried with
//     backoff for 3 s in total, then fails with E_SHARING_VIOLATION. A holder
//     that opened the file with FILE_SHARE_DELETE does not block: the rename
//     falls back to POSIX semantics (Windows 10 1809+, NTFS). A read-only
//     target fails at once with E_READONLY.
//   - KiCad locks honoured. Before writing, the KiCad lock files ("~<base>.lck")
//     of the target, of the project and (for a schematic) of the root sheet are
//     checked; a lock blocks with E_KICAD_LOCKED unless the BreakPolicy allows
//     removing it. KiCad does not keep the file open while editing, so the lock
//     file is the only signal that a GUI session would overwrite our write.
//   - Compare-and-swap. With Opts.Base set (from Read/Stat), the target is
//     re-hashed just before the rename and a mismatch fails with
//     E_FILE_CHANGED.
//   - Multi-file transactions. Txn stages every new file first, checks a
//     revision (sha256 over paths and base/new hashes), backs up the current
//     files under <root>/.pcbpilot/txn/, records a journal, renames each file
//     and restores the backups if any step fails. A journal left by a crash
//     blocks later transactions with E_TXN_PENDING until Recover or Discard.
//
// TOCTOU window: the lock check and the base re-hash happen immediately
// before the rename, not atomically with it. A writer that replaces the file
// (or KiCad that starts and saves) between that check and the rename —
// typically well under a millisecond, longer while a Windows rename is being
// retried — is overwritten without detection. No operating-system lock closes
// this window because KiCad itself neither holds nor checks one when it saves.
//
// Callers own process-level serialization: AcquireWriter makes one pcbpilot
// writer per project directory.
package kfile
