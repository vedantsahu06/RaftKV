# Failure Scenarios

> **Status:** Finalized crash and cleanup analysis for the approved architecture.
> Each scenario states the responsible component, pre-crash durable state,
> post-crash files, retention, recovery behavior, forbidden outcomes and startup
> result.
>
> **Platform note:** the Windows directory-entry durability mechanism is not yet
> validated. Scenarios that depend on it describe the required logical behavior,
> not a tested Windows guarantee. See
> [RECOVERY_AND_DURABILITY.md](RECOVERY_AND_DURABILITY.md) §9.

## 1. Assumptions and format

Unless a scenario says otherwise:

- a successful WAL `Sync` makes the containing batch durable, subject to verified
  platform behavior;
- a WAL segment is established only after temp write, header Sync,
  non-overwriting rename and directory-entry durability;
- an SSTable is durably created only after complete write, file Sync, full
  validation, close, non-overwriting rename and directory-entry durability;
- a Manifest publication is durable only after canonical payload plus CRC
  trailer, temp Sync, non-overwriting rename and directory-entry durability;
- rename within the supported filesystem operation is atomic;
- after reboot, an existing file may still be incomplete or corrupt;
- a higher version number alone does not prove pre-crash durable publication;
- acknowledged writes must survive;
- unacknowledged writes may be present or absent;
- an active WAL is never authorized for deletion;
- automatic generation switching has exactly two triggers, both checked before
  write admission: projected Memtable usage reaching the configured switching
  threshold, and appending the complete encoded record exceeding the configured
  WAL segment-size limit;
- an empty Memtable, startup, shutdown, flush completion and timers do not
  independently trigger switching;
- an empty generation is valid, produces no SSTable and introduces no special
  WAL deletion rule;
- a WAL or SSTable needed by any retained Manifest is retained;
- three committed Manifest versions are retained after cleanup, with a transient
  fourth allowed during publication;
- readers acquire ReadView references under a short-held `sync.RWMutex` and
  release them exactly once; the published ReadView owns its captured resources;
- shutdown is caller-context controlled; context expiry stops only the caller’s
  wait and never forces resource closure;
- Windows directory-entry durability is required but not assumed to be verified.

Each scenario reports:

- **Responsible** — the component that owns recovery or cleanup;
- **Durable before crash** — what had reached a durability boundary;
- **Files after crash** — what may be present;
- **Retain/delete** — what recovery must keep and what may be removed;
- **Recovery** — required action;
- **Must never** — forbidden outcomes;
- **Startup** — succeeds, fails, or is conditional.

## 2. Scenarios

### 1. Crash during WAL temporary-file creation

- **Responsible:** WAL Writer for segment creation; recovery scanner for temporary-file handling.
- **Durable before crash:** the old WAL tail and all previously synced records.
- **Files after crash:** a partial `wal-%020d.wal.tmp`; no final new segment; the old final segment remains.
- **Retain/delete:** retain old segments and the authoritative Manifest; the temp file is not a published segment and may be ignored or cleaned.
- **Recovery:** ignore or remove the temp file. No write was accepted into it. Use the old segment state.
- **Must never:** treat the temp file as a published segment; accept writes into it; delete old data because a temp exists.
- **Startup:** succeeds if the old WAL and Manifest state are valid.

### 2. Crash after WAL header Sync but before rename

- **Responsible:** WAL Writer; recovery scanner.
- **Durable before crash:** the complete temp-file header; the old WAL state.
- **Files after crash:** a valid-looking temp file; no new final segment; the old segment remains.
- **Retain/delete:** retain old segments and Manifest; ignore or clean the temp file.
- **Recovery:** no accepted write existed in the unrenamed segment. Continue from the old durable tail.
- **Must never:** treat header-synced temp content as a published segment; assume writes were accepted.
- **Startup:** succeeds.

### 3. Crash after WAL rename but before directory-entry durability

- **Responsible:** WAL Writer; recovery scanner.
- **Durable before crash:** the old WAL tail. New file contents were synced, but the new directory entry’s durability is uncertain.
- **Files after crash:** old final segment; possibly the new final segment, possibly only a temp, possibly neither new name.
- **Retain/delete:** retain the old segment and Manifest. The new segment is not established and may be ignored or cleaned. It is not a valid empty generation because its directory entry was never established.
- **Recovery:** because writes were accepted only after directory durability, no acknowledged record exists in the new segment. Do not use it to bridge a sequence gap. Create a fresh active segment later.
- **Must never:** infer that the rename was durable; accept writes into an unestablished segment; delete old WAL based on the new name; treat an unestablished segment as an empty generation.
- **Startup:** succeeds if the old state is valid.

### 4. Crash during WAL record write

- **Responsible:** WAL Writer for the failed write; WAL recovery for the incomplete tail.
- **Durable before crash:** all records in previously synced batches; possibly none of the current batch.
- **Files after crash:** the newest segment may end with a partial length field, header, key/value or checksum.
- **Retain/delete:** retain all prior records and segments; truncate only the incomplete tail at its starting offset and sync the file.
- **Recovery:** detect EOF-short data in the newest segment, truncate the incomplete record, sync, and replay prior records above the selected watermark.
- **Must never:** replay the partial record; synthesize bytes; truncate a sealed segment; treat an incomplete record as a complete checksum failure.
- **Startup:** succeeds after tail truncation if all prior records remain valid.

### 5. WAL Sync failure

- **Responsible:** WAL Writer marks Failed; Engine propagates fail-stop and requires close/reopen.
- **Durable before crash:** all batches with prior successful `Sync` calls. The failed batch’s durability is uncertain.
- **Files after crash:** the WAL may contain none, part or all of the failed batch.
- **Retain/delete:** retain the WAL and all recovery data. Do not delete affected segments.
- **Recovery:** WAL enters Failed, rejects new writes, fails affected in-flight and queued requests, and does not retry or seek past the failure. After reopen, validate records; complete valid records may be replayed even though unacknowledged.
- **Must never:** assume the failed batch is absent; retry the same `Sync` and continue; acknowledge affected writes; continue normal writes without recovery.
- **Startup:** after explicit close/reopen, succeeds if remaining files validate; client outcomes for the failed batch are uncertain.

### 6. Crash after WAL Sync but before Memtable application

- **Responsible:** Engine apply path; WAL recovery on reopen.
- **Durable before crash:** the full WAL batch containing the record.
- **Files after crash:** complete, valid WAL records; no in-memory Memtable.
- **Retain/delete:** retain the WAL segment and records. No WAL deletion is authorized because the generation was not durably published.
- **Recovery:** replay records above `FlushedThroughSeq` in ascending sequence order into recovery generations.
- **Must never:** treat the lack of a client response as proof that the record was not durable; skip the record; delete its WAL.
- **Startup:** succeeds; the write may be present even though unacknowledged.

### 7. Memtable application failure

- **Responsible:** Engine fail-stop path; WAL recovery on reopen.
- **Durable before crash:** the WAL record was synced; Memtable application failed.
- **Files after crash:** complete WAL records; Memtable contents are in memory and may be partially updated.
- **Retain/delete:** retain the WAL and all durable generation data; do not release an immutable Memtable as though a flush succeeded.
- **Recovery:** Engine Failed. Reject new reads and writes, fail affected requests, do not skip the failed sequence, do not apply later sequences. Close/reopen and recover from WAL.
- **Must never:** acknowledge the failed record; apply a later sequence past the failed one; treat the partially modified Memtable as authoritative.
- **Startup:** after recovery, succeeds if WAL and Manifest state validate; otherwise fails and preserves files.

### 8. Crash during generation switching

- **Responsible:** Engine switch coordinator; recovery reconciles durable WAL and Manifest state.
- **Durable before crash:** old WAL records that completed `Sync`; the old authoritative Manifest. The new Memtable and active-generation installation are in memory.
- **Files after crash:** old WAL sealed or still active depending on the step; possibly a new WAL temp or final segment; no durable new Memtable.
- **Retain/delete:** retain old WAL and all unflushed data. A new WAL with no acknowledged writes may be ignored or cleaned if not established. Retain the old Manifest; switching alone publishes no Manifest.
- **Recovery:** replay valid WAL above the selected watermark. A segment wholly above the watermark becomes one recovery generation; a straddling segment is fatal. An established empty segment remains a valid empty generation and produces no SSTable. Do not admit writes until the generation state is consistent.
- **Must never:** lose an admitted write; reject an admitted write merely because the gate closed; infer a generation from a filename alone; accept writes into an ambiguous generation; create an empty SSTable for an empty generation; treat the switch as triggered by startup, shutdown, flush completion or a timer.
- **Startup:** conditional. Succeeds if Manifest and WAL form a consistent sequence; fails and preserves files otherwise.

### 9. Crash while an old Memtable is being flushed

- **Responsible:** Engine flush coordinator and SSTable builder; recovery reconciles the orphan and WAL.
- **Durable before crash:** old WAL records; old authoritative Manifest. The SSTable may be absent, partial, synced, valid or an unpublished orphan.
- **Files after crash:** old WAL; an SSTable in any creation stage; a Manifest that may still omit it.
- **Retain/delete:** retain old WAL until durable publication. Retain a valid orphan until reconciliation. A partial orphan may be removed only when source data remains and the orphan policy permits it.
- **Recovery:** do not consider the flush complete; do not blindly adopt the orphan. Rebuild from WAL or reconcile using proof under the reconstruction predicate. Replay unflushed records.
- **Must never:** delete the old WAL; release Engine Memtable ownership; treat a synced or valid SSTable as published without Manifest publication.
- **Startup:** succeeds if WAL and old Manifest are valid and replay restores the state; fails and preserves files if the orphan is required and cannot be proven.

### 10. Crash during SSTable file creation

- **Responsible:** SSTable builder under Engine flush coordination.
- **Durable before crash:** old WAL and Manifest; no published new SSTable.
- **Files after crash:** a partial or incomplete `sstable-%06d.sst.tmp`, possibly an unresolved final name; old WAL; old Manifest.
- **Retain/delete:** retain old WAL and Manifest. The partial SSTable is not published and must not be adopted. It may be discarded or rebuilt only when source data remains.
- **Recovery:** flush is incomplete. Replay from WAL or rebuild from the retained generation. Do not trust partial metadata or footer.
- **Must never:** publish the partial file; read it as part of the database; delete the WAL before a replacement publication.
- **Startup:** succeeds if WAL replay restores the source state; otherwise fails and preserves files.

### 11. Crash after SSTable Sync but before Manifest publication

- **Responsible:** Engine flush coordinator and Manifest publisher; recovery reconciles the orphan.
- **Durable before crash:** SSTable contents were synced and possibly validated; old WAL; old Manifest. Rename or directory durability may not have completed.
- **Files after crash:** a synced/valid SSTable temp or final file; old WAL; old Manifest without the SSTable.
- **Retain/delete:** retain the orphan, old WAL and old Manifest. Do not delete WAL until durable publication.
- **Recovery:** do not treat the flush as complete. Rebuild/reconcile from WAL. A valid SSTable alone does not prove committed membership.
- **Must never:** adopt the orphan blindly; consider flush complete; authorize WAL deletion; release Engine Memtable ownership.
- **Startup:** succeeds after WAL replay if source data is intact; conditional if the orphan is required and cannot be reconciled.

### 12. Crash during Manifest temporary-file creation

- **Responsible:** Manifest publisher.
- **Durable before crash:** the current authoritative Manifest and all data it references.
- **Files after crash:** a partial `MANIFEST-%020d.tmp`; the current Manifest remains.
- **Retain/delete:** retain the current Manifest and referenced files; ignore or clean the temp file.
- **Recovery:** publication did not complete. Select the highest valid Manifest under the hierarchy.
- **Must never:** treat the temp file as authoritative; delete data based on it; overwrite an existing final Manifest.
- **Startup:** succeeds if the current Manifest and referenced SSTables validate.

### 13. Crash after Manifest rename but before directory-entry durability

- **Responsible:** Manifest publisher; recovery selects the highest consistent surviving version.
- **Durable before crash:** the old Manifest and its data; the new Manifest contents were synced, but the new directory entry’s durability is uncertain.
- **Files after crash:** old Manifest and possibly the new versioned Manifest, or only one of them.
- **Retain/delete:** retain all possible Manifest versions, all SSTables referenced by any of them, and all WAL needed by the retained set.
- **Recovery:** try versions in descending order. If the new version survived and is valid, including every referenced SSTable, it may be selected. Otherwise fall back. A surviving filename is not proof of durable publication.
- **Must never:** use filename ordering as proof of durability; delete an older Manifest or fallback data before the new publication is established; skip a referenced missing SSTable.
- **Startup:** conditional. Succeeds from the highest consistent surviving version; fails if no version or reconstruction is provable.

### 14. Crash after durable Manifest publication but before WAL deletion

- **Responsible:** Engine cleanup coordinator; WAL Writer performs authorized physical deletion.
- **Durable before crash:** the new Manifest, including its watermark and SSTable metadata; the corresponding WAL still exists.
- **Files after crash:** new Manifest; SSTable; extra WAL segments not yet deleted.
- **Retain/delete:** retain the new Manifest and SSTable. Extra WAL is safe to retain. Delete only when the oldest retained Manifest watermark covers its maximum sequence and the exact generation authorization is satisfied.
- **Recovery:** select the Manifest, validate SSTables, skip WAL at or below `FlushedThroughSeq`, replay required records above it. Extra WAL is not a correctness problem.
- **Must never:** assume extra WAL means the flush failed; replay records below the watermark as new writes; delete WAL without the five conditions.
- **Startup:** succeeds if the retained Manifest/SSTable state and remaining WAL validate.

### 15. Crash during WAL deletion

- **Responsible:** WAL Writer after Engine authorization; recovery checks retained Manifests and watermarks.
- **Durable before crash:** Manifest and SSTable publication; deletion authorization may have been issued. Some authorized segments may be gone.
- **Files after crash:** some authorized WAL segments missing, remaining segments present; deletion may not yet be directory-durable.
- **Retain/delete:** retain all remaining WAL required by any retained Manifest. Missing ranges are safe only when covered by the oldest retained Manifest watermark and deleted under exact authorization.
- **Recovery:** use the selected Manifest watermark to determine whether missing ranges are represented. A missing segment with required sequence above the watermark is fatal.
- **Must never:** delete the active segment; delete a segment needed by a fallback Manifest; infer safety from “oldest WAL” or segment-number order.
- **Startup:** succeeds if all required ranges are represented; otherwise fails and preserves remaining files.

### 16. Crash during Manifest version cleanup

- **Responsible:** Engine cleanup coordinator and Manifest component; recovery validates the retained set.
- **Durable before crash:** a newly published Manifest and possibly four transient versions. The retired version may or may not have been unlinked and directory-durable.
- **Files after crash:** three or four Manifest versions; SSTables and WAL for all still-retained versions; possibly a version whose deletion was not durable.
- **Retain/delete:** retain every Manifest that may still be a fallback and every SSTable/WAL it references. Do not delete data whose protection depends on whether the old version’s removal was durable.
- **Recovery:** select the highest valid version; treat extra versions as retained until cleanup is proven. Resume cleanup only after the authoritative state is established.
- **Must never:** assume the old Manifest is gone; delete fallback WAL/SSTables before Manifest deletion is durable; enforce “exactly three at every instant” by deleting before publication.
- **Startup:** succeeds if at least one consistent version exists; fails if none exists and reconstruction is not provable.

### 17. Crash during SSTable cleanup

- **Responsible:** Engine cleanup coordinator; recovery validates all Manifest-referenced SSTables.
- **Durable before crash:** retained Manifests and their referenced SSTables; unreferenced SSTables may be in the process of deletion.
- **Files after crash:** some unreferenced SSTables gone, some remaining; all referenced SSTables must remain.
- **Retain/delete:** retain every SSTable referenced by any retained Manifest. Unreferenced files may remain safely; retry deletion only after confirming the retained set.
- **Recovery:** validate referenced SSTables; ignore surplus unreferenced files unless the reconstruction predicate needs them. Do not adopt them blindly.
- **Must never:** delete a referenced SSTable; treat an unreferenced valid file as published; infer publication from file existence.
- **Startup:** succeeds if all retained Manifest references validate.

### 18. Corruption of the newest Manifest

- **Responsible:** Manifest recovery path in the Engine.
- **Durable before crash:** previous valid Manifest versions and their referenced SSTables/WAL.
- **Files after crash:** a corrupt newest Manifest; older retained versions may be valid.
- **Retain/delete:** retain the corrupt file for diagnosis, all older versions and all data they reference. Do not delete data based on the corrupt file.
- **Recovery:** fail validation of the newest, then try the next older version and continue. A fallback may require WAL above the corrupt version’s watermark; retain it.
- **Must never:** partially parse the corrupt Manifest and continue; silently ignore broken references; delete fallback data; initialize empty.
- **Startup:** succeeds if a previous version and required files are consistent; otherwise reconstruction or failure.

### 19. Corruption of every available Manifest

- **Responsible:** Manifest recovery and reconstruction path.
- **Durable before crash:** possibly valid SSTables and WAL, but no valid Manifest.
- **Files after crash:** corrupt Manifest versions; SSTables; WAL segments; possibly temp files.
- **Retain/delete:** preserve all files. No automatic deletion during this condition.
- **Recovery:** attempt reconstruction only if fully valid SSTables, source WAL evidence, one-to-one generation mappings and contiguous sequence coverage prove completeness. Otherwise fail and preserve files.
- **Must never:** initialize an empty database; adopt all valid SSTables; replay arbitrary WAL without proving coverage; delete files.
- **Startup:** conditional. Succeeds only with provable reconstruction; otherwise fails.

### 20. Missing or corrupted Manifest-referenced SSTable

- **Responsible:** Manifest recovery path and SSTable validation.
- **Durable before crash:** the Manifest that references the SSTable; other versions may reference different SSTable sets.
- **Files after crash:** the referenced SSTable is missing, corrupt, checksum-invalid or metadata-mismatched; other files may be intact.
- **Retain/delete:** retain the Manifest and all other referenced files. Do not delete the missing SSTable’s WAL; it may be the recovery source. Do not substitute an orphan blindly.
- **Recovery:** the selected Manifest is unusable; fall back to a previous retained version if consistent. If no fallback is recoverable, fail startup. Never skip the reference.
- **Must never:** read as if the SSTable were empty; ignore the missing key range; use an orphan without proof; initialize empty.
- **Startup:** conditional. May succeed through a consistent fallback; otherwise fails.

### 21. Incomplete final WAL record

- **Responsible:** WAL recovery.
- **Durable before crash:** all complete records before the incomplete record. The incomplete record was not acknowledged.
- **Files after crash:** the newest segment ends with a partial length, header, key/value or checksum.
- **Retain/delete:** retain the valid prefix; truncate only the incomplete record at its starting offset and sync the truncated segment. Retain all sealed segments.
- **Recovery:** detect EOF-short data in the newest segment, truncate, sync, and replay records above the watermark.
- **Must never:** truncate a sealed segment; treat the fragment as a complete record; continue into later bytes.
- **Startup:** succeeds if the remaining prefix is valid.

### 22. Complete WAL record with invalid checksum

- **Responsible:** WAL recovery.
- **Durable before crash:** all preceding validated records. The corrupt record may have been acknowledged before later damage.
- **Files after crash:** a complete-length record whose CRC32C does not match.
- **Retain/delete:** retain the segment and all database files. Do not truncate, rewrite or delete.
- **Recovery:** fail startup with the segment ID, offset and sequence where available. Do not treat it as an incomplete tail. Preserve files for investigation.
- **Must never:** truncate as if incomplete; skip the record and replay later records; continue normal writes.
- **Startup:** fails.

### 23. Corruption in the middle of a WAL segment

- **Responsible:** WAL recovery.
- **Durable before crash:** the durable prefix before the corruption; later bytes may include acknowledged records.
- **Files after crash:** invalid framing or checksum before the segment end; possibly valid-looking bytes after it.
- **Retain/delete:** retain the entire segment and all files. Do not delete or truncate using the corruption offset.
- **Recovery:** fail startup. Do not resynchronize, scan for magic, skip to a later record or continue into later data.
- **Must never:** silently drop the corrupt record and later records; assume the suffix is unacknowledged; initialize empty.
- **Startup:** fails.

### 24. Graceful shutdown with pending writes

- **Responsible:** Engine shutdown coordinator and WAL Writer.
- **Durable before crash:** all batches that completed `Sync`; accepted writes may still be in the active batch or awaiting application.
- **Files after crash:** the active WAL may contain a partially written final batch; completed writes remain; Memtable state is lost.
- **Retain/delete:** retain the WAL and Manifest; do not rotate or delete segments during shutdown. Recovery handles the final batch.
- **Recovery:** if shutdown completed successfully, every accepted write was drained, synced and applied before handles closed. If not, recovery truncates an incomplete newest tail and replays above the watermark. A reported failure is not hidden. A caller-context expiry is reported to that caller, but shutdown itself continues.
- **Must never:** acknowledge a write not synced and applied; close WAL handles while an admitted write depends on them; claim all accepted writes succeeded after a reported failure; treat context expiry as permission to force-close handles.
- **Startup:** succeeds after normal recovery if the resulting state is valid. A prior `Close(ctx)` failure or context expiry is reported to its caller and does not by itself invalidate the recovered state.

### 25. Shutdown concurrent with generation switching

- **Responsible:** Engine shutdown and switch coordinator; WAL Writer for admitted writes.
- **Durable before crash:** the old generation’s synced WAL; any new segment that completed creation; the old authoritative Manifest. The new Memtable/active generation may be in memory only.
- **Files after crash:** old WAL possibly sealed; new WAL possibly present or absent; old Manifest; no guarantee that the new generation was installed durably.
- **Retain/delete:** retain old WAL and unflushed records; retain any new WAL that may contain admitted writes; retain the old Manifest.
- **Recovery:** replay WAL above the selected watermark, validate sequence continuity and establish a consistent active state. Shutdown waits for the in-progress switch under the final 12-step sequence; shutdown does not independently trigger a switch.
- **Must never:** close WAL handles under an admitted write; reopen admission after shutdown begins; lose an acknowledged write; assume a generation exists because a filename exists; start a new switch solely because shutdown began.
- **Startup:** conditional. Succeeds if recovery proves a consistent state; otherwise fails and preserves files.

### 26. SSTable rename succeeds but directory-entry durability fails

- **Responsible:** SSTable builder and filesystem backend.
- **Durable before crash:** the SSTable contents were synced and fully validated; the rename may have occurred, but the final directory entry is not confirmed durable.
- **Files after crash:** possibly the final `sstable-%06d.sst`, possibly only the temp file, possibly neither; the old WAL and Manifest remain.
- **Retain/delete:** retain the WAL, Memtable and Manifest. The final-named file is an unconfirmed orphan, not a published SSTable.
- **Recovery:** do not publish through the Manifest. Rebuild from WAL if the file is absent or unconfirmed; retain an orphan without adopting it blindly. Report the durability failure.
- **Must never:** treat the rename as durable; reference the file in a Manifest; delete the WAL; claim flush success.
- **Startup:** succeeds if WAL replay restores the generation; otherwise conditional or failure while preserving files.

### 27. Windows directory-entry durability operation is unsupported or fails

- **Responsible:** filesystem backend and the component requesting durability.
- **Durable before crash:** file contents may be synced, but the directory entry’s persistence is not established. The logical publication step is incomplete.
- **Files after crash:** the new file may exist or not; the previous authoritative state remains.
- **Retain/delete:** retain the previous WAL, SSTable and Manifest state. Do not delete source data. The new file may remain as an orphan.
- **Recovery:** the requesting operation reports failure. WAL segment creation is not established; SSTable publication is not performed; Manifest publication is not successful. Recovery uses the previous durable state and any valid surviving files.
- **Must never:** silently treat the operation as Linux `fsync`; report success; publish a Manifest that references an unconfirmed SSTable; accept writes into an unconfirmed WAL segment.
- **Startup:** succeeds only from a previously valid state. If the platform cannot provide the required operation, Phase 1 support for that configuration is not claimed.

### 28. Memtable reservation cannot fit within the hard limit

- **Responsible:** Engine admission and reservation accounting.
- **Durable before crash:** all previously admitted and synced writes. The rejected write has no WAL record.
- **Files after crash:** the WAL contains only admitted writes; the Memtable is in memory.
- **Retain/delete:** retain all durable state. No cleanup is caused by the rejection.
- **Recovery:** if the projected usage reaches the switching threshold, switch before admitting the write and admit it into the new generation. If the reservation still cannot fit within the hard limit, admission backpressures until capacity is released, or returns a resource error when the caller’s pre-admission context ends. A request larger than the hard limit is rejected explicitly. The hard limit is never exceeded.
- **Must never:** over-admit; scan the whole Memtable to find space; apply a write without a reservation; switch because the Memtable is empty or because a timer fired; claim success for a rejected write.
- **Startup:** not applicable to the rejection; the database remains consistent. A later crash recovers the durable WAL.

### 29. Reader holds a ReadView while a Memtable is published and released

- **Responsible:** ReadView publication coordinator and Engine publication path.
- **Durable before crash:** the SSTable is durably published and the new ReadView is installed; the old ReadView and its Memtable may still be referenced by readers.
- **Files after crash:** the published SSTable and Manifest; no Memtable survives a process crash.
- **Retain/delete:** retain the SSTable while any retained Manifest references it. Old ReadView resources are reclaimed only after the old view’s reference count reaches zero. File cleanup waits for views that can reference the SSTable.
- **Recovery:** a reader that captured the old ReadView keeps its Memtable and metadata alive; a reader that captures after publication uses the new ReadView and SSTable view. The reader never sees a mix that omits both sources.
- **Must never:** reclaim a Memtable or SSTable resource while any ReadView references it; delete an SSTable while an active ReadView can use it; install the new ReadView before durable publication; let a read return a version above its captured sequence; release a ReadView reference more than once or omit release when the key is found early.
- **Startup:** not applicable; this is a runtime lifetime scenario. A crash recovers from Manifest and WAL.

### 30. SSTable ID scan finds an orphan or temporary file

- **Responsible:** Engine startup ID allocator and SSTable scanner.
- **Durable before crash:** the Manifest’s `NextSSTableID`; observed final and temporary SSTable filenames.
- **Files after crash:** valid final/temp SSTable files, possibly unreferenced; malformed `sstable-` names may also exist.
- **Retain/delete:** consume the ID of every valid final or temp filename, even if corrupt. Do not overwrite or reuse it. Malformed names establish no ID and are not treated as published.
- **Recovery:** compute `NextID = max(Manifest.NextSSTableID, HighestObservedSSTableID + 1)`. Persist the updated counter in a later Manifest publication. Retain orphans until cleanup proves safety.
- **Must never:** reuse an observed orphan/temp ID; overwrite an existing SSTable; adopt a valid orphan blindly; silently delete a malformed file.
- **Startup:** succeeds if the selected Manifest and referenced SSTables validate; malformed unreferenced names are reported but do not become database state.

### 31. Shutdown encounters a running flush failure

- **Responsible:** Engine shutdown coordinator and flush coordinator; Manifest publisher.
- **Durable before crash:** the old WAL and immutable Memtable source data; the previous authoritative Manifest. The failed flush may have produced a temp or orphan SSTable.
- **Files after crash:** old WAL; possibly a partial, validated or orphan SSTable; Manifest without the new table; three or four Manifest versions if publication was in progress.
- **Retain/delete:** retain the WAL, immutable Memtable data (until process exit), failed SSTable artifacts and previous Manifest. Do not delete source data or claim the flush succeeded.
- **Recovery:** shutdown reports the flush error. On reopen, Manifest fallback and WAL replay restore the generation. An orphan may be rebuilt or reconciled under proof.
- **Must never:** delete the WAL because a flush was attempted; publish a partial table; report successful shutdown for the failed flush; release unflushed data as if durable.
- **Startup:** succeeds if WAL and the retained Manifest state are valid; otherwise fails and preserves files.

### 32. Manifest checksum failure

- **Responsible:** Manifest recovery path.
- **Durable before crash:** the Manifest file contents may be complete or truncated, but the CRC32C trailer does not match the canonical payload.
- **Files after crash:** the invalid Manifest version; older retained versions; SSTables and WAL.
- **Retain/delete:** preserve the invalid version for diagnosis; retain all older versions and data they reference. Do not delete data based on the invalid file.
- **Recovery:** reject the invalid version without partial parsing. Try the next older valid version, then reconstruction. Never accept a payload because it parses.
- **Must never:** ignore a checksum mismatch; use partially decoded SSTable metadata; delete fallback data; initialize empty.
- **Startup:** conditional. Succeeds from a valid older version or proven reconstruction; otherwise fails and preserves files.

### 33. WAL segment straddles FlushedThroughSeq

- **Responsible:** WAL recovery and Manifest selection.
- **Durable before crash:** a published prefix represented in SSTables and a WAL segment containing records both at or below and above the selected watermark.
- **Files after crash:** the selected Manifest; SSTables covering part of the segment’s range; the WAL segment with a mixed range.
- **Retain/delete:** retain the Manifest, SSTables and entire WAL segment. Do not split, truncate or delete the segment to fit the watermark.
- **Recovery:** fail recovery for that state. A generation must be represented as a whole or not at all. Do not replay only the upper part or skip the lower part.
- **Must never:** partially replay a generation; assume the segment is wholly published; truncate the lower records; initialize empty.
- **Startup:** fails for that state and preserves files.

### 34. Reconstruction finds ambiguous evidence

- **Responsible:** reconstruction path in the Engine.
- **Durable before crash:** no valid Manifest; multiple SSTables, WAL segments or generation claims that do not form one provable state.
- **Files after crash:** final and temporary SSTables, WAL segments, corrupt Manifest versions.
- **Retain/delete:** preserve every file. Do not adopt, delete or rewrite anything during the failed reconstruction attempt.
- **Recovery:** examples include two valid SSTables claiming one source generation, an adopted SSTable whose source WAL is absent, source WAL records that differ from the SSTable, a sequence gap, or an overlap that cannot be explained. Fail startup.
- **Must never:** pick the “newest” or “largest” SSTable by heuristic; ignore a conflicting source WAL; replay from an arbitrary sequence; delete files; initialize empty.
- **Startup:** fails and preserves files for manual recovery.

### 35. SSTable ID exhaustion or conflicting ID

- **Responsible:** Engine SSTable ID allocator.
- **Durable before crash:** the Manifest `NextSSTableID`; existing final and temporary SSTable names; all source WAL and Memtable data for a pending flush.
- **Files after crash:** existing SSTable files; the WAL and Memtable source data; no new SSTable if allocation failed.
- **Retain/delete:** retain all files and source data. Never wrap, reuse or overwrite an ID. The ID range is `1..999,999` in v1.
- **Recovery:** if an allocated ID conflicts with an observed file or Manifest metadata, fail the flush and preserve files. If the range is exhausted, fail safely and require operator intervention. No silent ID reuse.
- **Must never:** wrap to 1; overwrite an existing SSTable; allocate an ID already present in a Manifest or filename; delete the WAL to make room.
- **Startup:** succeeds with the existing valid state, but new flushes cannot proceed until the ID condition is resolved; if the condition prevents proving the selected state, fail.

### 36. WAL request rejected before admission

- **Responsible:** WAL Writer admission and Engine request validation.
- **Durable before crash:** all previously admitted and synced records. The rejected request has no WAL record and no sequence.
- **Files after crash:** only previously admitted WAL records exist.
- **Retain/delete:** retain all durable state; no deletion is caused by rejection.
- **Recovery:** a full queue applies backpressure. An oversized record, empty key, invalid delete, or key above 1 MiB is rejected before enqueue. A record at or below 16 MiB total must be accepted or explicitly backpressured, not split across batches.
- **Must never:** assign a sequence to a rejected request; write a partial record; silently drop an admitted request; over-admit beyond the queue capacity.
- **Startup:** not applicable to the rejection; a later crash recovers only admitted records.

### 37. Manifest publication with conflicting SSTable metadata

- **Responsible:** Manifest publisher.
- **Durable before crash:** the previous authoritative Manifest and its referenced files.
- **Files after crash:** a temp or final Manifest attempt that conflicts with an existing SSTable ID; older Manifest versions; the SSTable files.
- **Retain/delete:** never overwrite the existing Manifest or SSTable. Retain the conflicting attempt for diagnosis and the previous authoritative state.
- **Recovery:** reject matching-ID publication when filename, size, key bounds, sequence bounds, checksum, level or source WAL ID differ. Retry with a new Manifest version only for a truly identical logical state.
- **Must never:** silently replace metadata for an existing ID; duplicate the same SSTable entry; treat a conflicting attempt as successful; delete the older Manifest before a valid new one is durable.
- **Startup:** succeeds from the previous valid Manifest if its references validate; otherwise fallback or fail.

### 38. Caller context expires during shutdown while shutdown continues

- **Responsible:** Engine shutdown coordinator and the caller.
- **Durable before crash:** the same state as the shutdown step that was in progress; all previously synced WAL batches remain durable.
- **Files after crash:** any WAL, SSTable or Manifest file whose publication step had begun; the shutdown continues operating on these files after the caller has returned.
- **Retain/delete:** shutdown retains all data required by the established durability and recovery rules. Context expiry does not authorize deletion, forced handle closure or cancellation of a flush that still owns resources.
- **Recovery:** the caller receives a context or timeout error. Shutdown continues in the background, completes or reports its own operation failures, and a later `Close` may wait for the same shutdown. If the process exits before shutdown finishes, crash recovery handles the durable state.
- **Must never:** treat context expiry as shutdown cancellation; forcibly close WAL or SSTable handles still owned by a background operation; report successful closure because the caller timed out; abandon a Manifest publication or flush in a state that violates its protocol.
- **Startup:** succeeds after normal recovery if the durable state is valid; otherwise fails and preserves files. The context error says nothing about whether shutdown eventually succeeded.

### 39. Empty generation is sealed or left unflushed at shutdown

- **Responsible:** Engine switch and shutdown coordination; WAL recovery.
- **Durable before crash:** an established WAL segment with a valid header and zero records; the previous Manifest watermark. No sequence range belongs to the generation.
- **Files after crash:** the empty WAL segment and older retained files; no SSTable for the empty generation.
- **Retain/delete:** retain the empty segment and all retained Manifest/SSTable state. The five-condition rule does not authorize deletion because the generation has no maximum sequence, and no special empty-generation deletion rule exists.
- **Recovery:** treat the segment as a valid empty generation: no replay records, no `FlushedThroughSeq` advance and no SSTable. Continue recovery without creating an empty table.
- **Must never:** create an empty SSTable; infer a sequence range; advance the watermark; delete the segment under a special rule; trigger a switch merely because the generation is empty.
- **Startup:** succeeds if the remaining Manifest and WAL state validate; an empty generation is not corruption.

### 40. Write cannot fit within the configured WAL segment limit

- **Responsible:** Engine admission and WAL segment-size trigger.
- **Durable before crash:** all previously admitted and synced records. The rejected record has no WAL framing and no sequence.
- **Files after crash:** the existing active or sealed segments; no partial record for the rejected write.
- **Retain/delete:** retain all durable state. The rejection causes no deletion or switch loop.
- **Recovery:** if appending the complete encoded record would exceed the active segment limit, switch first and admit the waiting write into the new generation’s WAL. If the record cannot fit within a fresh configured segment, reject it explicitly with a resource/limit error.
- **Must never:** split a record across segments; rotate the WAL independently of the generation switch; switch repeatedly for a record that can never fit; assign a sequence to the rejected record.
- **Startup:** not applicable to the rejection; the database remains consistent and prior WAL recovery is unaffected.

## 3. Cross-scenario retention summary

| Data | May be deleted only when |
|---|---|
| Active WAL | Never |
| Sealed WAL | All five conditions hold: durable SSTable publication, sealed segment, oldest retained Manifest watermark covers its maximum sequence, exact generation mapping and explicit Engine authorization |
| Empty WAL segment | Not authorized by the five-condition rule because it has no sequence range; no special empty-generation deletion rule exists. Retain it unless a future approved cleanup policy addresses empty segments |
| Valid unpublished SSTable | Not referenced by any retained Manifest and source data proves deletion cannot lose acknowledged data; otherwise retain |
| Published SSTable | Not referenced by any retained Manifest and cleanup ordering is durable |
| Retired Manifest | A newer authoritative Manifest is durably published and the remaining fallback set is sufficient |
| WAL temp file | Never a published segment; may be cleaned when no accepted write depends on it |
| Manifest temp file | Never authoritative; may be cleaned after the authoritative version is established |
| Partial SSTable | Not published; may be cleaned or rebuilt only with sufficient source data |
| Invalid final SSTable | Never adopted; preserve for diagnosis unless completeness is otherwise proven and cleanup is safe |

## 4. Forbidden recovery shortcuts

The following are never valid recovery strategies:

- initialize empty because recovery failed;
- adopt every valid SSTable found on disk;
- replay all WAL from sequence 1 without proving coverage;
- truncate a complete record with a bad checksum;
- skip a corrupt middle record and continue;
- delete WAL because a newer Manifest exists;
- delete SSTables because the newest Manifest does not reference them;
- treat a filename as proof of durable publication;
- ignore a missing Manifest-referenced SSTable;
- retry a failed WAL `Sync` in place and continue;
- apply a later sequence after a Memtable application failure;
- partially replay a WAL segment that straddles the watermark;
- trigger a generation switch from an empty Memtable, startup, shutdown, flush
  completion or a timer;
- create an empty SSTable for an empty generation;
- forcibly close resources because the caller’s shutdown context expired;
- treat context expiry as cancellation of shutdown;
- claim Windows directory-entry durability without a validated mechanism.

## 5. Platform limitations

These scenarios require atomic same-volume rename, non-overwriting publication
rename and directory-entry durability. On Windows these operations must be
implemented through the filesystem abstraction and validated on local NTFS.

The exact Windows mechanism, its crash behavior and power-loss behavior are
implementation validation requirements. No scenario claims a stronger guarantee
than the platform actually provides. Network drives and filesystems without the
required guarantees are outside Phase 1.

