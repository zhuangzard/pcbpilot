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
//   - KiCad locks honoured, failing closed. Before writing (and before each
//     rename retry) the KiCad lock files ("~<base>.lck") of the target, of the
//     project and (for a schematic) of the root sheet are checked; a lock
//     blocks with E_KICAD_LOCKED unless the BreakPolicy allows removing it, and
//     so does any failure to establish that no lock exists. KiCad does not keep
//     the file open while editing, so the lock file is the only signal that a
//     GUI session would overwrite our write.
//   - Compare-and-swap. With Opts.Base set (from Read/Stat), the target is
//     re-hashed just before each rename attempt and a mismatch fails with
//     E_FILE_CHANGED.
//   - Multi-file transactions. Txn stages every new file first, checks a
//     revision (sha256 over paths and base/new hashes), holds the project
//     writer lock, records a journal, makes durable backups under
//     <root>/.pcbpilot/txn/, and only then touches the targets, re-checking
//     locks and base hashes right before each one. If a step fails, every
//     target already touched (including the failing one) that still holds
//     what the transaction wrote is restored with its original mode. The
//     journal is removed before the backups, so a journal always has its
//     backups. A journal left by a crash blocks later transactions with
//     E_TXN_PENDING until Recover (restore what the transaction wrote; finish
//     a transaction that had fully landed; refuse, changing nothing, if a file
//     was edited since; a file whose bytes are back but whose mode is not
//     gets its mode) or Discard (keep the files as they are). Recover and a
//     rollback flush every journalled file and directory before the journal
//     goes; if a flush fails the journal and backups stay. Staged files stay
//     0600 and are re-hashed before the commit and before each rename; the
//     txn tree is 0700 with 0600 backups; the writer lock and the txn tree
//     cannot themselves be staged or deleted. Symlinks, reparse points (except
//     cloud-sync placeholders such as OneDrive files, which are the real
//     file) and other non-regular targets are refused (E_UNSUPPORTED_TARGET), at
//     staging and again before each change. Every install is chmod, flush,
//     rename, flush the directory; before any journal is dropped (recover,
//     rollback, discard) the recorded final modes are verified and every
//     file is flushed.
//   - One writer per project. AcquireWriter is an operating-system lock on
//     <root>/.pcbpilot/writer.lock (Windows share mode, Unix flock), released
//     by the kernel when the holder dies; Apply, Recover and Discard take it.
//
// Durability on Unix: file contents are fsynced before they are renamed
// into place; every directory whose entries change (target directories, the
// txn and backup directories, and the parents of directories Stage created,
// bottom-up to the first one that already existed) is fsynced, and a failed
// fsync is an error. Each backup is hashed after copying and must equal the
// base hash before the journal says "commit".
//
// Durability on Windows is best effort. Windows cannot flush a directory
// handle, so whether a newly created directory entry (a backup, the
// journal, a created directory or file) survives a power loss depends on
// NTFS metadata journaling. What pcbpilot does: every file it creates is
// flushed (FlushFileBuffers) before it is used; targets and the journal are
// replaced with MoveFileExW WRITE_THROUGH; the POSIX-rename fallback flushes
// the renamed file. Remaining windows: after a power cut, a just-created
// backup or journal entry may be missing even though its data was flushed
// (so a journal may be lost, leaving targets half-applied with no automatic
// recover), and on non-NTFS volumes (FAT/exFAT, some network shares) none
// of the metadata ordering is guaranteed.
//
// TOCTOU window: the lock check and the base re-hash happen immediately
// before each rename, not atomically with it. A writer that replaces the file
// (or KiCad that starts and saves) between that check and the rename —
// typically well under a millisecond — is overwritten without detection. No
// operating-system lock closes this window because KiCad itself neither holds
// nor checks one when it saves. Likewise a stale lock is re-read (size,
// mtime, content) and the KiCad process list re-checked right before it is
// deleted; a KiCad that rewrites it within those microseconds loses its lock.
package kfile
