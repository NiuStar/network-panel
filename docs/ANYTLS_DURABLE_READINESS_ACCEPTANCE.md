# AnyTLS durable readiness fix acceptance

## Incident and scope

The subscription gate must retain a real boot barrier followed by a fresh same-process successful installed-certificate confirmation. In v2.0.0.32 that state is read only from `node_anytls_cert_log`, while scheduler retention deletes those diagnostic rows by time and by per-node count. A healthy long-running Agent eventually loses its only boot barrier and is excluded from export.

The fix must persist bounded authenticated process-readiness state independently from rotating diagnostic logs. It must NOT make a fresh installed check without a witnessed boot sufficient, nor weaken fingerprint, CA, hostname, certificate lifetime, two-minute report freshness, new-boot or newer-failure invalidation.

## Baseline regression evidence

`golang-backend/internal/app/scheduler/anytls_retention_test.go` exercises the real `pruneOldDataOnce` and actual authenticated Shadowrocket renderer with a locally generated CA/leaf and dummy-only credentials. It first proves a strict pinned export, removes the boot diagnostic by row cap or time retention, and requires the export to remain ready.

Identical test bytes on isolated baseline commit `2688f6b236eebb4ae1b88b22c7e9219bac2a4c6d` fail both cases at the post-pruning business assertion. The initial fixture attempt failed authentication because JWT_SECRET was unset; that result is not counted as intended RED. Retained logs: `.hermes-retention-baseline-red.log` (fixture issue) and `.hermes-retention-baseline-red2.log` (actual regression).

## Migration and operational boundary

- Do not seed or reconstruct a missing boot event from an installed-only report. Lost diagnostic evidence remains unavailable.
- The existing Agent emits authenticated `agent_boot` followed by `post_reload` installed confirmation at every WebSocket connection (Agent main.go connection path). After an approved controller cutover, normal reconnection should establish valid durable state without restarting Agent/GOST.
- A complete real retained boot/confirmation chain may be imported only under a reviewed race-safe fail-closed cold-start path. No invented rows, credential-bearing raw payload copy or permissive DB-error fallback.
- Dedicated readiness state is bounded per node, does not store certificate private keys, passwords or arbitrary raw log JSON, and must not be subject to generic diagnostic retention.
- Old diagnostic cleanup remains active; disabling retention is not the permanent solution.

## Candidate gates

1. Baseline RED then candidate GREEN for actual time and row-cap pruning.
2. Authenticated ingestion atomically updates audit/state; event ordering uses server receive time plus a unique ID. Database failures and partial writes fail closed.
3. New/malformed boots, newer failures, mismatched process, wrong leaf, invalid/expired certificates, stale/future confirmations and malformed reports do not export a pin.
4. Reopen persistent DB and exercise concurrent/delayed writers; stale migration must not resurrect readiness.
5. Run controller/scheduler/DB tests and broader Go test/vet; compile actual backend. Independently review the frozen diff.
6. Before production, build immutable image and test isolated DB/port/storage. Production endpoint/container replacement and restart require explicit confirmation; this work does not authorize them.
7. After approved cutover, read back exact durable rows, fresh same-process boot/confirmation, actual authenticated exports for nodes20/21, and real forwarding. Do not label source/test completion as deployed recovery.

## Verified implementation and parent executions

- Production state is `anytls_runtime_state`, one bounded row per authenticated node. `internal/anytlsreadiness.Persist` takes the server-authenticated node ID and atomically appends a sanitized audit plus advances state; the authenticated WS closes on persistence failure and local reads latch closed until a stored authentic boot.
- Runtime export uses `Load` + `Ready` and the original certificate/key/CA/hostname/lifetime gates. Revision lookup failures now fail closed rather than silently selecting revision zero.
- Cold compatibility is read-only real retained evidence, not a durable import performed by export. A missing boot stays unready; the first authentic WS event locks/imports existing complete witnesses then updates state atomically. Tests for steady state ingest via `Persist`.
- Parent actual full Go run: 187 top-level PASS, one explicitly skipped subprocess helper, no failed packages/tests. `go vet ./...` passed. Final affected scheduler regression additionally passes complete diagnostic removal, failure-log removal retaining invalidation, and new-boot invalidation surviving log deletion; an old process cannot restore readiness. One durable state row remains.
- Final regression bytes selected against the exact old gate with Go overlay fail both row-cap and time-window post-pruning assertions (`.hermes/readiness-evidence/parent-identical-final-red.log`). This runs the real renderer/cleanup with current ingestion; it is distinct from the initial baseline fixture evidence.
- Actual `cmd/server` candidate built and executed twice with an isolated disposable SQLite DB and owned ephemeral port. New-table migration, 401 anonymous subscription rejection, absence of fabricated state rows, repeated startup and owned listener cleanup passed (`native-startup-smoke.json`). Its `.32` version string is inherited source metadata, NOT a published new release.
- SQLite cross-process locking/reopen/rollback tests passed. MySQL production dialect concurrency has NOT been exercised; no MySQL daemon or Docker is available in this environment. Process-local failed-write quarantine cannot convey an uncommitted event to another controller or survive a controller restart; no stronger distributed guarantee is claimed.
- Existing scheduler fixture tests emit missing unrelated-table diagnostic messages on the baseline; they pass unchanged. The first native smoke attempt selected a nonexistent route and failed its expected-status assertion; corrected subscription route passed, with owned child/listener cleanup in both paths.
- No production update, release tag, image publication, production DB write or Agent/GOST restart occurred.

## Independent review closeout

Independent review verdict: `PASS_SOURCE_REVIEW_AND_LOCAL_SQLITE_ACCEPTANCE_NOT_DEPLOYED`. Complete production source manifest SHA256: `91705bf4a0d04a1f0db17f0e0decd53db762cb368f70e38e11fef7ce95b35d8a` (101 files); parent readback verified every file unchanged after review.

Reviewer independently executed the full backend suite (187 top-level PASS, one expected helper skip), full vet, real server build and 20 repetitions of SQLite cross-process contention/reopen tests. Added independent overlay checks cover cold negative-query errors, malformed boot, barrier/replay ordering, failure ordering and malformed quarantine recovery. No production blocker was found within the current single-fixed-pool architecture.

The pool+node quarantine limitation remains explicit: another database pool or another controller cannot observe an invalidation that failed to commit. Multi-controller deployment or pool replacement requires re-evaluation, not an inherited stronger guarantee. Real MySQL and race-detector acceptance remain unexecuted.

The isolated native smoke receipt's `anonymous_node_list_http` field is a historical harness label; the corrected actual requested path is `/api/v1/subscription/shadowrocket` and it returned 401. This smoke proves real process/schema/startup/anonymous rejection, not authenticated subscription business acceptance. The authenticated renderer is separately exercised in regression tests.

No production cutover or image release is implied by this review. After this code branch is published, the next release gate is isolated MySQL/immutable-image acceptance, followed by explicit production-switch confirmation and authenticated post-cutover reconnect/subscription/forwarding readback.

## Remaining production state

At diagnosis nodes20/21 have matching fresh installed checks but their boot logs are gone. No production repair, certificate migration, readiness-row insertion or restart is performed by this code task. Production subscriptions remain unverified until a separately approved rollout and authenticated reconnect/readback.
