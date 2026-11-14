# Decision Register

> **Status key**
>
> - **Finalized** — approved architecture contract.
> - **Open** — genuine unresolved policy. None currently; the status is retained
>   for future decision history. See [OPEN_QUESTIONS.md](OPEN_QUESTIONS.md) for
>   implementation validation items.
> - **Validation required** — semantics are final, but the platform or
>   implementation evidence is not yet established.
>
> This register is the permanent architectural decision record. The component
> documents define the detailed obligations. There are no active architecture
> conflicts. The former C-001 and C-002 conflicts are resolved by D-074 and
> D-075, and their history is retained in §11.

## 1. Scope and ownership

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-001 | Phase 1 is a single-node persistent LSM-tree storage engine with Put, Get and Delete. Raft, replication, snapshots, compaction, Bloom filters, benchmarking and release work are later phases. | Finalized | Keeps the first phase focused on local crash consistency. |
| D-002 | WAL owns append-only segments, framing, checksums, sequence assignment, batching, group commit, sync, rotation execution, recovery scanning, incomplete-tail handling and physical deletion after authorization. | Finalized | One writer owns the durability path and file lifetime. |
| D-003 | Engine owns request validation, write admission and ordering, Memtable application, acknowledgement, generations, flush coordination, durable publication decisions, WAL deletion authorization and lifecycle. | Finalized | Safety decisions require knowledge of Memtables, SSTables and Manifest state. |
| D-004 | Memtable owns in-memory key-value storage, sequence-ordered application, concurrent reader safety, immutability after seal and lifetime retained by published ReadViews. | Finalized | The Memtable is the application point between WAL durability and read visibility. |
| D-005 | SSTable owns immutable sorted on-disk storage, data blocks, sparse index, metadata, checksums and footer. | Finalized | Separates immutable storage from metadata publication. |
| D-006 | Manifest owns authoritative published-SSTable metadata, format version, `FlushedThroughSeq`, per-SSTable metadata and `NextSSTableID`. It contains no key-value records. | Finalized | A single authoritative metadata source is required for recovery. |
| D-007 | A valid SSTable file is not automatically a published SSTable. Manifest publication determines membership. | Finalized | File existence and structural validity do not prove committed membership. |
| D-008 | The future Raft log is not declared to be the Phase 1 WAL. Reusing this WAL as a Raft log requires separate review. | Finalized | Preserves a clear protocol boundary. |

## 2. Write path and acknowledgement

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-009 | The WAL Writer assigns sequence numbers. Sequence assignment and WAL enqueue order are identical. | Finalized | Removes a race between independent sequence allocation and append ordering. |
| D-010 | A caller’s `Append` returns only after the corresponding group batch completed a successful WAL `Sync`. | Finalized | Establishes a clear durability boundary. |
| D-011 | Client acknowledgement requires successful WAL `Sync` and successful Memtable application. WAL append success alone is not client success. | Finalized | A durable record that is not applied is not a completed database write. |
| D-012 | Memtable application follows ascending sequence order regardless of WAL completion order. | Finalized | Preserves prefix consistency and monotonic visibility. |
| D-013 | One batch write is followed by one `Sync`. Requests arriving during the write/Sync belong to a later batch. Initial WAL defaults are recorded in D-066. | Finalized | Group commit amortizes Sync without splitting the durability boundary. |
| D-014 | Context cancellation may prevent admission before enqueueing; an admitted write cannot be cancelled halfway through WAL processing. | Finalized | Avoids a durable record with no committed application path. |
| D-015 | A WAL write or `Sync` failure is fail-stop: mark Failed, reject new writes, fail affected in-flight/queued requests, do not retry in place, require close/reopen and recovery. | Finalized | After an uncertain Sync, no later writes can safely rely on the WAL state. |
| D-016 | A failed or uncertain `Sync` does not mean the data is absent. | Finalized | The durability outcome is unknown; recovery must inspect the files. |
| D-017 | Memtable application failure after WAL `Sync` is Engine fail-stop. Do not skip the failed sequence or apply later sequences. | Finalized | Later application would violate prefix consistency. |

## 3. WAL format and segments

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-018 | WAL format is finalized: little-endian; CRC32C Castagnoli; 20-byte segment header with magic `RKWL`, version, flags, segment ID and header CRC; 28-byte fixed record overhead with length, type, flags, reserved, sequence, key length, value length, key, value and trailing CRC. | Finalized | Provides an exact on-disk contract. |
| D-019 | Record types are Put = 1 and Delete = 2. No other types are approved for v1. | Finalized | Matches the initial API; future types require review. |
| D-020 | Validation: empty keys rejected; Put values may be empty; Delete value length zero; sequences start at 1 and increase by exactly one; max record 16 MiB; max key 1 MiB; invalid types/flags/reserved/lengths rejected. | Finalized | Prevents ambiguous records and oversized recovery allocations. |
| D-021 | WAL directory is `<db>/wal/`; filename is `wal-%020d.wal`; temporary name is `wal-%020d.wal.tmp`; segment IDs begin at 1 and increase monotonically. | Finalized | Lexical order matches numerical segment order. |
| D-022 | Segment creation uses an exclusive temporary file, complete header, header Sync, non-overwriting atomic rename to the final name, directory-entry durability, then writes are accepted. | Finalized | Prevents accepting writes into a segment whose name/header is not durably established. |
| D-023 | WAL segment size default is 64 MiB, configurable. Appending a complete encoded record that would exceed the configured limit is a pre-admission trigger for a coordinated generation switch, not independent rotation. | Finalized default | WAL rotation and Memtable switching must occur together. |
| D-024 | Initial batch defaults are 1,024 records and a 1 MiB batch byte target. The byte value does not cap an individual record; a larger record forms its own batch up to the 16 MiB record limit. | Finalized defaults | Configurable throughput/latency trade-off, not a performance guarantee. |
| D-025 | Active WAL accepts appends and is never authorized for deletion. Sealed WAL accepts no appends. | Finalized | Deleting the active tail would destroy the recovery source. |
| D-026 | Existing segments are sealed after recovery and a fresh active segment is created; recovery does not append to a recovered file. | Finalized | Avoids appending after a truncated tail and makes the active boundary explicit. |

## 4. Recovery and corruption

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-027 | Recovery enumerates, validates and sorts WAL segments; rejects duplicate IDs; validates headers, lengths, checksums, types, flags and sequence ordering; detects missing required segments and gaps. | Finalized | Prevents silent replay of an inconsistent history. |
| D-028 | Replay only records above the selected Manifest’s `FlushedThroughSeq`. | Finalized | Already published records must not be applied as new writes. |
| D-029 | Only the newest WAL segment may have an incomplete tail eligible for truncation. Truncate at the record start and sync the file. | Finalized | A sealed segment should be complete; a newest tail may be an unacknowledged partial write. |
| D-030 | A complete record with a bad checksum is fatal corruption, not an incomplete tail. | Finalized | It may have been acknowledged; silent truncation could lose committed data. |
| D-031 | Middle-of-file corruption is fatal; no resynchronization, magic scanning, skipping or continuation. | Finalized | Length-framed records cannot be safely resynchronized after corruption. |
| D-032 | Unknown versions, invalid fields, malformed records, sequence duplicates/decreases/gaps and invalid sealed segments are fatal. | Finalized | Recovery must not guess a valid history. |
| D-033 | The next sequence is one greater than the highest recovered sequence or durable flushed watermark, whichever is greater. | Finalized | Avoids reusing a sequence represented in durable state. |
| D-034 | Never silently initialize an empty database when existing database artifacts are present but recovery fails. | Finalized | Prevents catastrophic data loss disguised as first startup. |

## 5. Generations, Memtables and switching

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-035 | One WAL segment belongs to exactly one Memtable generation. | Finalized | Keeps generation ownership and deletion authorization unambiguous. |
| D-036 | Normal flush produces exactly one SSTable publication for one non-empty generation. Records of a generation are not split across multiple SSTables. | Finalized | Provides a stable generation-to-SSTable mapping. |
| D-037 | WAL rotation and Memtable switching happen together. Automatic switching has exactly the two triggers in D-081, both checked before write admission; the WAL never rotates independently. | Finalized | Prevents a WAL segment from spanning generations. |
| D-038 | Switching closes admission, lets already-admitted writes finish WAL Sync and application, seals the old WAL, creates a new WAL and Memtable, installs the new active generation, then reopens admission. | Finalized | Preserves the old generation while establishing the new one. |
| D-039 | A generation cannot be sealed until all admitted writes finish. Closing the gate does not reject or cancel admitted writes. | Finalized | Prevents losing writes whose WAL records may already be durable. |
| D-040 | Use a configurable switching threshold below the hard maximum. Under the admission lock, reserve an incremental estimate and require `ActualUsage + ReservedBytes + NewReservation <= HardLimit`. The estimate includes key, value, object/entry, Skip List node and associated overhead plus a safety margin. Do not scan the whole Memtable. A projected threshold crossing is evaluated before admission under D-081. | Finalized; estimator calibration is validation required | Protects admitted writes without repeated full scans. |
| D-041 | Memtable lifetime is owned through ReadViews. A published ReadView holds an ownership reference over all resources captured by that view. Engine ownership of an immutable Memtable may be released after durable SSTable publication and installation of a new ReadView; the old view and its Memtables remain usable until that view’s reference count reaches zero. | Finalized; D-083 refines the mechanism | Prevents use-after-free while allowing flush progress. |
| D-042 | The Memtable uses a Skip List. Internal keys are ordered by user key ascending and sequence descending. Tombstones are retained. | Finalized | Resolves the former data-structure and visibility gap. |

## 6. SSTable and publication

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-043 | SSTables are immutable and use the finalized block-based v1 format: 36-byte header, sorted data blocks, sparse index, metadata block and 40-byte footer, with CRC32C checksums and no compression. | Finalized | Provides an exact implementable format. |
| D-044 | SSTable creation order: exclusive temp file, complete write, file Sync, full read-back validation, close, non-overwriting atomic rename, directory-entry durability, then Manifest publication. | Finalized | A file must be complete, valid and durably named before authoritative membership. |
| D-045 | If Manifest publication fails, retain the SSTable, immutable Memtable and corresponding WAL; do not consider flush complete; retry or reconcile safely. | Finalized | The WAL remains the recovery source until publication succeeds. |
| D-046 | Do not blindly adopt orphan SSTables. Adoption requires trustworthy retained source WAL/generation evidence under the reconstruction predicate. | Finalized | File validity does not prove committed membership. |
| D-047 | Manifest SSTable metadata includes stable ID, filename, level, file size, smallest key, largest key, minimum sequence, maximum sequence, whole-file CRC32C and source WAL segment ID. | Finalized | Provides identification, bounds, integrity and provenance. |
| D-048 | Do not store a separate `published=true` flag in the SSTable. | Finalized | Publication is Manifest authority, not a file-local claim. |
| D-049 | Full integrity validation is required during SSTable creation and during recovery for every Manifest-referenced SSTable, including all data blocks and checksums. | Finalized | Prevents publishing or accepting a partially valid table. |

## 7. Manifest

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-050 | Manifest updates never directly overwrite the authoritative Manifest. | Finalized | Preserves an authoritative fallback during publication. |
| D-051 | Manifest publication: canonical payload plus CRC trailer, temp write, file Sync, non-overwriting atomic rename to a new versioned filename, directory-entry durability, then report durable success. | Finalized | Directory-entry durability is required for the renamed entry. |
| D-052 | Manifest publication is logically idempotent: matching SSTable ID/metadata is accepted without duplication; conflicting metadata is an error. A retry uses a new Manifest version and never overwrites a file. | Finalized | Retries must not duplicate or silently replace metadata. |
| D-053 | Retain the latest three committed Manifest versions after cleanup. A transient fourth is allowed during crash-safe publication. Recovery considers every available version, highest first. | Finalized — C-001 resolved | Provides fallback without delete-before-publish. |
| D-054 | Recovery tries newest, previous and every retained Manifest, then reconstruction; if reconstruction is not provable, fail startup and preserve files. | Finalized | Prevents silent empty initialization or arbitrary adoption. |
| D-055 | Never delete an SSTable referenced by any retained Manifest. | Finalized | Fallback must remain recoverable. |
| D-056 | A WAL segment is eligible for deletion only when all five conditions hold, including the oldest retained Manifest’s `FlushedThroughSeq >= WAL.MaxSeq`. | Finalized — C-002 resolved | An older fallback may still require WAL records. |
| D-057 | Manifest, SSTable and WAL cleanup are coordinated. Manifest cleanup precedes deletion of data protected only by the retired version. Cleanup scheduling is an implementation detail. | Finalized safety ordering | Fallback data spans all three file classes. |
| D-058 | A successful platform directory-entry durability operation is the durable publication boundary. Filename ordering alone does not prove durable publication. | Finalized; Windows mechanism validation required | Rename durability is not established by file presence alone. |

## 8. WAL deletion, shutdown and lifecycle

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-059 | Engine authorizes deletion of the exact generation-owned WAL; WAL validates existence, sealed state and sequence range, then deletes and performs WAL directory-entry durability. | Finalized — C-002 resolved | Separates the safety decision from the physical operation. |
| D-060 | WAL deletion authorization identifies segment ID, minimum sequence and maximum sequence. The WAL does not revalidate SSTables, Manifest or full checksums. | Finalized | Avoids duplicating Engine authority and expensive validation. |
| D-061 | A logically incorrect but internally consistent authorization cannot be detected by the WAL; correct generation mapping is an Engine invariant. | Finalized | Defines the trust boundary explicitly. |
| D-062 | Obsolete-segment deletion failure after durable publication and fallback eligibility is a cleanup failure, not a WAL writer failure. Retain and retry later. | Finalized | The data is already safely published. |
| D-063 | Graceful shutdown is caller-controlled, conceptually `Close(ctx context.Context) error`. It follows the 12-step sequence: reject reads/writes, close admission, wait for switch, drain admitted writes, stop new flushes, finish running flushes and publication, retain failed-flush data, wait for readers, close handles, return errors. Context expiry stops only the caller’s wait; shutdown continues. | Finalized — extended by D-082 | Does not claim success for incomplete writes. |
| D-064 | Shutdown must not claim all accepted writes completed if WAL write/Sync, Memtable application or flush failed. | Finalized | Failure reporting must match actual durability and application. |
| D-065 | Context expiry or timeout does not authorize forced resource closure. Shutdown continues in the background; a subsequent `Close` may wait for the same operation; successful closure is never reported while background operations still own resources. | Finalized — AQ-002 resolved by D-082 | Preserves lifecycle and durability guarantees under caller cancellation. |

## 9. Finalized review decisions

| ID | Decision | Status | Rationale |
|---|---|---|---|
| D-066 | WAL operational defaults are 64 MiB segments, 1,024 records per batch, a 1 MiB batch byte target, a 4,096-request queue and a 200-microsecond collection interval. The byte target is not a per-record cap; a larger record forms its own batch. All are configurable initial values. | Finalized defaults | Establishes concrete starting parameters without performance claims. |
| D-067 | `GenerationID = WAL segment ID`. There is no separate durable generation counter or registry. SSTable IDs are independent. Each SSTable records source GenerationID and source WAL segment ID; Manifest metadata records the source WAL segment ID. | Finalized | Provides one durable generation identity without another registry. |
| D-068 | Each ReadView captures one `read_sequence`, one consistent active/immutable Memtable set and one immutable published-SSTable metadata view. The published ReadView owns the captured resources. Lookups return the newest version at or below `read_sequence`. | Finalized; D-083 refines the mechanism | Provides point-in-time reads without a separate snapshot subsystem. |
| D-069 | Flush publication is serialized in generation/seal order so `FlushedThroughSeq` remains a contiguous prefix. | Finalized | Preserves fallback and watermark correctness. |
| D-070 | SSTable v1 uses little-endian encoding, a 36-byte header, 4 KiB target data blocks, framed blocks (`uint32 length + payload + CRC32C`), a sparse index, a metadata block, a 40-byte footer, no compression and a whole-file CRC32C recorded in the Manifest. | Finalized | Defines the exact implementable physical format. |
| D-071 | SSTable creation uses an exclusive temp file, complete write, file Sync, full read-back validation, close, non-overwriting atomic rename, directory-entry durability, then Manifest publication. | Finalized | Ensures the Manifest never references an unconfirmed file. |
| D-072 | Every Manifest-referenced SSTable is fully validated during recovery, including every data block and checksum. Missing, invalid or mismatched references make the Manifest unusable and trigger fallback. | Finalized | Prevents silent acceptance of incomplete state. |
| D-073 | Manifest encoding is deterministic compact JSON with a 4-byte little-endian CRC32C trailer. The checksum covers the payload only. Field order, canonical numbers/base64, filenames `MANIFEST-%020d` and temp `.tmp` names are fixed. No `CURRENT` file exists. | Finalized | Independent implementations compute the same payload and checksum. |
| D-074 | Retain three committed Manifest versions after cleanup; allow a transient fourth during crash-safe publication. | Finalized — C-001 resolved | Makes three a steady-state target, not a literal maximum. |
| D-075 | WAL deletion requires all five conditions: durable SSTable publication, sealed segment, oldest retained Manifest watermark coverage, exact generation mapping and explicit Engine authorization. | Finalized — C-002 resolved | Prevents deleting WAL still needed by fallback. |
| D-076 | `NextSSTableID = max(Manifest.NextSSTableID, HighestObservedSSTableID + 1)`, scanning final and temporary SSTable names. Never reuse an observed ID. Persist the counter in a later Manifest publication. | Finalized | Prevents overwriting or reusing orphan IDs. |
| D-077 | Reconstruction is allowed only when completeness is proven: fully valid SSTables, valid source generation/WAL identities, one-to-one mappings, no gaps or conflicts, replayable WAL evidence and an unambiguous watermark. Otherwise fail and preserve files. | Finalized | Prevents heuristic salvage and silent data loss. |
| D-078 | Engine Failed rejects new reads and writes. In-flight reads return an error if failure is detected before completion; every completed read releases its ReadView reference exactly once. | Finalized | Avoids returning potentially inconsistent results while preventing leaks. |
| D-079 | Shutdown follows the approved 12-step sequence and does not force-flush every Memtable; unflushed writes remain WAL-recoverable. | Finalized | Provides deterministic lifecycle without inventing forced shutdown. |
| D-080 | Phase 1 initially targets Windows 10/11 local NTFS behind a filesystem abstraction covering temp creation, writing, file Sync, atomic same-volume rename, directory-entry durability, deletion and deletion durability. Linux is future work; network drives are outside Phase 1. | Finalized; Windows mechanism validation required | Makes the platform target explicit without assuming Linux semantics. |
| D-081 | Automatic generation switching has exactly two triggers, both checked before admitting the write. (1) Memtable: `ActualUsage + ReservedBytes + NewReservation` reaches or exceeds the configured switching threshold. (2) WAL: appending the complete encoded record would exceed the configured segment-size limit. The waiting write is admitted into the new generation after switching. The hard Memtable limit and the WAL segment limit reject writes that cannot fit. Empty Memtables, startup, shutdown, flush completion and timers do not trigger switching; no other automatic trigger exists. An empty generation remains valid, produces no SSTable and introduces no special WAL deletion rule. | Finalized — AQ-001 resolved | Removes unbounded generation growth and empty-table ambiguity without adding timer or lifecycle side effects. |
| D-082 | Graceful shutdown is caller-controlled, conceptually `Close(ctx context.Context) error`. Context expiry stops only the caller’s wait; shutdown continues in the background and is never converted into forced resource closure. Resources are closed only when no background operation still owns them. A subsequent `Close` may wait for the same shutdown. Shutdown never reports successful closure while background operations still own resources, and operation failures are reported while durability and recovery guarantees are preserved. | Finalized — AQ-002 resolved | Bounds the caller’s wait without weakening lifecycle or durability safety. |
| D-083 | ReadView synchronization uses a short-held `sync.RWMutex` plus ReadView-level reference counting. Reader: `RLock`, capture the published view, increment its reference while still holding `RLock`, release `RLock`, perform the whole read on that view, then release exactly once. Publisher: construct a complete immutable ReadView, take the write lock, install it as current, release the old view’s ownership reference, unlock, and reclaim the old view and resources only at reference count zero. Readers reference the ReadView, not individual Memtables or SSTables. | Finalized — AQ-005 resolved | Gives safe acquisition without per-table reader bookkeeping or use-after-free during publication. |

## 10. Open and validation items

These are not missing decisions hidden in code. They are explicitly tracked in
[OPEN_QUESTIONS.md](OPEN_QUESTIONS.md). No open architectural policy remains.

| Item | Type | Summary |
|---|---|---|
| AQ-003 | Validation required | Windows directory-entry durability mechanism |
| AQ-004 | Validation required | Memory estimator calibration and safety margin |
| AQ-006 | Validation required | Crash and power-loss verification on Windows/NTFS |

AQ-005 is resolved by D-083. Its implementation validation obligations remain:
reference-count synchronization, SSTable file-handle and resource lifetime,
applied-sequence visibility, deterministic reader-lifetime tests, concurrent
switching/publication/read tests, race-detector and stress tests, and
reference-leak and premature-reclamation checks. They are tracked in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) Milestones 8 and 10 and are not
an open architectural policy.

## 11. Resolved review items

These items were previously tracked as conflicts or unresolved details. They are
resolved and must not be reopened merely because their history is recorded here.

| Former item | Resolution |
|---|---|
| C-001 — three Manifest versions versus transient fourth | D-074: three after cleanup, transient fourth allowed during publication |
| C-002 — deletion after newest publication versus oldest retained watermark | D-075: five-condition rule including oldest retained Manifest coverage |
| U-001 — Memtable structure and visibility | D-042, D-068 |
| U-002 — generation identity durability and recovery | D-067; empty generations resolved by D-081 |
| U-003 — reservation algorithm and limits | D-040; estimator calibration remains AQ-004 |
| U-004 — SSTable filename and durable creation protocol | D-044, D-070, D-071 |
| U-005 — SSTable physical format and checksum scope | D-043, D-070 |
| U-006 — recovery validation depth | D-049, D-072 |
| U-007 — Manifest naming, encoding and cleanup mechanics | D-051, D-073; cleanup scheduling is an implementation detail |
| U-008 — reconstruction predicate and orphan adoption | D-046, D-077 |
| U-009 — WAL group-commit defaults and temp naming | D-021, D-024, D-066 |
| U-010 — shutdown interaction with switches, flushes and readers | D-063, D-079; caller-context behavior resolved by D-082 |
| U-011 — read behavior after fail-stop | D-078 |
| U-012 — cross-process exclusion and platform durability | D-080; Windows mechanism remains AQ-003 and crash testing AQ-006 |
| U-013 — ReadView model and reader registration | D-068, D-083 |
| U-014 — Manifest checksum and stable-ID allocation | D-073, D-076 |
| AQ-001 — empty-generation handling | D-081: exactly two pre-admission switching triggers; empty generations remain valid, produce no SSTable and add no WAL deletion rule |
| AQ-002 — shutdown timeout or forced-shutdown policy | D-082: caller-controlled context; expiry never forces resource closure and shutdown continues |
| AQ-005 — ReadView synchronization primitive | D-083: short-held `sync.RWMutex` plus ReadView-level reference counting |

## 12. Invariant summary

The complete invariant list is in
[SYSTEM_ARCHITECTURE.md §8](SYSTEM_ARCHITECTURE.md#8-invariants). The most
important relationships are:

- sequence order is authoritative;
- acknowledgement requires WAL Sync and Memtable application;
- one WAL segment per generation and one SSTable publication per non-empty
  generation; empty generations remain valid and produce no SSTable;
- generation switching has exactly two pre-admission automatic triggers:
  projected Memtable usage at the switching threshold and the next complete WAL
  record exceeding the segment-size limit;
- GenerationID is the WAL segment ID; SSTable IDs are independent;
- Manifest publication, not file validity, determines membership;
- WAL deletion requires all five conditions, including oldest retained Manifest
  coverage;
- fallback retention protects WAL and SSTables;
- WAL write/Sync failure and Memtable application failure are fail-stop;
- Engine Failed rejects reads and writes;
- a published ReadView owns the captured resources; readers use ReadView-level
  reference counting under a short-held `sync.RWMutex` and reclaim only at zero;
- ReadViews cannot observe reclaimed Memtables or inconsistent publication;
- recovery must prove consistency or fail and preserve files;
- shutdown is caller-context controlled; expiry stops only the caller’s wait and
  never forces resource closure;
- Windows directory durability is never assumed without validation.
