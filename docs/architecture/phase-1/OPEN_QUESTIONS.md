# Open Questions and Validation Requirements

> **Purpose:** Track the implementation-time validation requirements that remain
> after the Phase 1 architecture review.
>
> AQ-001, AQ-002 and AQ-005 are resolved and recorded as D-081, D-082 and D-083
> in [DECISIONS.md](DECISIONS.md). The former conflicts C-001 and C-002 and the
> former unresolved details U-001–U-014 are also resolved. Those items are not
> open questions.
>
> No open architectural policy remains. Every item in this file is a
> **Validation required** item: the semantics are final, but the implementation
> evidence does not yet exist. No item here may be presented as a tested
> guarantee.

## 1. Implementation validation requirements

### AQ-003 — Windows directory-entry durability mechanism

**Status:** Validation required.

The logical publication protocols require directory-entry durability after file
creation/rename and after deletion. The exact Windows mechanism is not
established by the documentation.

Required validation:

- identify the Windows API sequence for persisting a directory entry after
  create, rename and delete on local NTFS;
- verify it against Microsoft documentation;
- prove it does not silently succeed when unsupported;
- verify non-overwriting atomic rename behavior for SSTable and Manifest
  publication;
- verify file `Sync` durability for WAL, SSTable and Manifest contents;
- verify deletion persistence;
- test process crash, system crash, reboot and abrupt power loss where feasible;
- reject network drives and unsupported filesystems rather than claiming support.

Until validated, the documentation must not state that Windows crash durability
is implemented or tested. See
[RECOVERY_AND_DURABILITY.md](RECOVERY_AND_DURABILITY.md) §9.

**Affected documents**

- [SYSTEM_ARCHITECTURE.md](SYSTEM_ARCHITECTURE.md)
- [WAL_DESIGN.md](WAL_DESIGN.md)
- [SSTABLE_DESIGN.md](SSTABLE_DESIGN.md)
- [MANIFEST_DESIGN.md](MANIFEST_DESIGN.md)
- [RECOVERY_AND_DURABILITY.md](RECOVERY_AND_DURABILITY.md)
- [FAILURE_SCENARIOS.md](FAILURE_SCENARIOS.md)
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md)

### AQ-004 — Memory estimator calibration and safety margin

**Status:** Validation required.

The accounting formula and the exactly-two generation-switching triggers are
final. The estimator still requires calibration.

```text
ActualUsage + ReservedBytes + NewReservation <= HardLimit
```

The estimate must include key, value, entry/object overhead, Skip List node
overhead, associated allocations and a conservative margin. No full-Memtable scan
is allowed.

Required calibration:

- measure actual Go allocation behavior for the chosen Skip List node layout;
- choose a formula and safety margin that never underestimate accounted usage;
- verify reservation conversion and release paths;
- test boundary records, large keys/values, tombstones and concurrent admission;
- define the hard limit and switching threshold defaults;
- verify that a projected crossing of the switching threshold is evaluated
  before admission;
- fail-stop if actual accounted usage exceeds the reservation.

The 100 MB / 98 MB example is illustrative, not a fixed requirement.

**Affected documents**

- [MEMTABLE_DESIGN.md](MEMTABLE_DESIGN.md)
- [CONCURRENCY_AND_LIFECYCLE.md](CONCURRENCY_AND_LIFECYCLE.md)
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md)

### AQ-006 — Crash and power-loss verification

**Status:** Validation required.

The documented crash behavior is a specification, not tested evidence.

Required validation:

- subprocess crash tests at every WAL, SSTable and Manifest publication boundary;
- crash after rename but before directory-entry durability;
- crash during cleanup and WAL/SSTable deletion;
- abrupt power-loss and reboot tests on local NTFS;
- recovery assertions that every acknowledged write survives;
- corruption injection for every checksum, length and framing field;
- reconstruction tests for valid, absent and conflicting source WAL evidence;
- confirmation that recovery never initializes empty over existing artifacts.

No durability guarantee may be reported as tested until this validation is
completed.

**Affected documents**

- [RECOVERY_AND_DURABILITY.md](RECOVERY_AND_DURABILITY.md)
- [FAILURE_SCENARIOS.md](FAILURE_SCENARIOS.md)
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md)

## 2. Resolved questions and validation follow-up

| Resolved item | Decision | Summary |
|---|---|---|
| AQ-001 | D-081 | Exactly two pre-admission automatic generation-switching triggers; empty generations remain valid and produce no SSTable |
| AQ-002 | D-082 | Caller-context graceful shutdown; expiry stops only the caller’s wait and never forces resource closure |
| AQ-005 | D-083 | Short-held `sync.RWMutex` plus ReadView-level reference counting |

AQ-005’s architecture is resolved, but its implementation validation obligations
remain: reference-count synchronization, SSTable file-handle/resource lifetime,
applied-sequence visibility, deterministic reader-lifetime tests, concurrent
switching/publication/read tests, race and stress tests, and reference-leak
checks. These are tracked in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) Milestones 8 and 10 and the
required ReadView and shutdown test classes. They are not an open architectural
question.

## 3. Items that are not open

The following are finalized and must not be treated as open:

- automatic generation switching has exactly two triggers, both checked before
  admission: projected Memtable usage reaching the configured switching
  threshold, and appending the next encoded WAL record exceeding the configured
  segment-size limit;
- empty Memtables do not trigger a switch; startup, shutdown, flush completion
  and timers do not independently trigger a switch;
- empty generations remain valid, produce no SSTable and introduce no special
  WAL deletion rule;
- the Memtable hard limit and the WAL segment limit reject writes that cannot
  fit;
- the Skip List Memtable and its internal key ordering;
- incremental estimated reservations under the admission lock;
- the WAL record format, record types and validation limits;
- WAL defaults of 64 MiB, 1,024 records, 1 MiB, queue 4,096 and 200 microseconds;
- `GenerationID = WAL segment ID` and independent SSTable IDs;
- ReadView capture, visibility and lifetime semantics, including short-held
  `sync.RWMutex` acquisition and ReadView-level reference counting;
- generation/seal-ordered flush publication;
- the SSTable v1 byte format and whole-file checksum;
- the SSTable durable creation protocol;
- full SSTable validation during recovery;
- canonical Manifest JSON, CRC32C coverage, filenames and no `CURRENT` file;
- three committed Manifest versions after cleanup with a transient fourth;
- the five-condition WAL deletion rule;
- SSTable ID allocation and no-reuse rules;
- the reconstruction completeness predicate;
- Engine Failed read/write rejection and in-flight read errors;
- caller-controlled graceful shutdown, including the 12-step sequence, no forced
  resource closure, and background completion after the caller’s context expires;
- Windows 10/11 local NTFS as the initial target;
- Linux as future work and network drives as out of scope.

## 4. Validation request

Before implementation claims the corresponding behavior is complete, the
implementation validation must answer:

1. AQ-003: Which Windows mechanism establishes directory-entry durability, and
   what did the tests prove?
2. AQ-004: What estimator formula and margin never underestimate usage, and does
   the pre-admission switching-threshold check use that estimate correctly?
3. AQ-006: What crash and power-loss tests were executed, and what did they
   establish?

In addition, the resolved AQ-005 validation obligations in
[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) must be completed. These items
must be reported as validation results, not as architectural decisions.
