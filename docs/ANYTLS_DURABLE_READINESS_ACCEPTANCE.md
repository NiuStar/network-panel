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

## Isolated MySQL 5.7 and image acceptance

Production was read-only identified as MySQL **5.7.44 / REPEATABLE-READ**. The builder `10.126.126.3` ran the same MySQL image ID `sha256:4bc6bc963e6d8443453676cae56536f4b8156d78bae03c0145cbe47c2aad73bb` on a private internal Docker network, with fresh QA-only databases/accounts. No production DB or state was copied.

Accepted implementation commit: `7d423770b5af9d5a868f7f522bdb2cf7c6745ab0`. All 101 production source files were compared with the independent reviewed manifest. The candidate server binary SHA256 is `1860dc2284dbd2243424f399759861d7b9a9369ba796a713a80c2f7fa57cbe81`.

The backend-only **amd64 QA image**, built on the exact previously released `.32` base, is `network-panel-readiness-qa:7d423770`, image ID `sha256:caf3c07843b12308749903babbc35fe46331b182b43e9391f1f84b04744988b1`. It has not been registry-published and is not a new version Release or a multi-architecture release. Frontend, install script and both amd64 downloadable Agent files remain byte-identical to the base. The inherited downloadable Agent bytes differ from the independently approved formal GitHub asset; this QA does not approve them for node replacement. Production auto-upgrade remains outside this acceptance.

Executed gates:

- Actual candidate MySQL startup/schema migration, anonymous subscription HTTP 401, `/api/v1/version`, `/flow/test`, `/app/`, and `/install.sh` HTTP checks passed. State table initially had zero rows, not fabricated readiness.
- Three fresh, empty QA databases each passed actual InnoDB acceptance: diagnostic row-cap/time deletion and complete audit deletion, retained invalidation, freshness/pin/process refusal, real held-row-lock release, final-write failure transaction rollback, eight independent OS writer processes/pools, stale-writer refusal, exact row counts and close/reopen persistence. No selected acceptance test was skipped.
- A separate actual candidate process passed authenticated WebSocket → MySQL durable state → real HTTP Shadowrocket export. After deleting all diagnostic rows, strict pinned export remained available. Reload failure and replacement boot invalidated export; deleting their diagnostics did not clear invalidation, and an old-process confirmation could not restore readiness. This uses a dummy Agent and generated fixture CA only, not real production nodes or public forwarding.
- Initial candidate HTTP probe incorrectly used a host-published port on an internal Docker network; retained failure was corrected to the private container IP. Initial WS fixture omitted the DB-backed certificate feature switch (an environment variable does not enable it); the failed attempt remains retained, and corrected acceptance used a new DB and container. Neither correction changed production code.
- All owned QA panel and MySQL containers were stopped after evidence collection; data and stopped containers were retained, not deleted. Unrelated running container IDs/images were checked unchanged. Builder free disk remained approximately 2.57 GB; no global prune or package install occurred.

Evidence is retained locally in `/workspace/network-panel-readiness-mysql-qa/final-builder-readback.json` (SHA256 `b9a57659919717315273551a00743eaeb2babe78d64547e074459b9cbced256a`) and on builder under `/workspace/np-readiness-qa-7d423770/`. The three DB test logs and corrected WS log are embedded in that readback with individual SHA checks. Earlier statements that MySQL was unexecuted describe the source-review stage; these new executions close that specific dialect/runtime gate. Race-detector and cross-controller failed-write-quarantine limitations remain unchanged.

## Production rollout and recovery state

The preceding source-review/QA statements are historical. The user explicitly accepted online backups (single-transaction InnoDB dump plus independent file archive; not an atomic cross-resource snapshot), then explicitly directed the assistant to connect to server28 and execute.

- Published and read back the backend-only amd64 reference `24802117/network-panel:readiness-7d423770b5af9d5a868f7f522bdb2cf7c6745ab0@sha256:caf3c07843b12308749903babbc35fe46331b182b43e9391f1f84b04744988b1`; no version/latest tag promotion. Production auto-upgrade remains false; no Agent assets were installed by this change.
- Server28 performed one apply using frozen script SHA `47fe7b7225a0ca0a76c72aa6658e6472370d90c6674fb0d1060ac225c62e6d87`. Fresh online backups preceded the persistent Compose image replacement. Compose recreated only backend; MySQL retained ID `2f137800dc624c010f44fe668dbb3c9e7cc2abbf436bfc51e2b6c5915133ebbd`.
- Candidate container `c9498e217a709f9ebb17ad61d6cac35198f6538956fa4ac4ded910e493be57fb`, started `2026-11-01T03:38:42.942711092Z`, remains running with restart_count=0 and exact reviewed server SHA. Auth rejection, version, frontend and five local assets passed. Effective Compose differs only in backend image; ports, certificate rows, mounts and remaining runtime configuration match the before-image.
- The original apply stopped at **HOLD**, before owner/verified receipts: environment entries were reordered without value differences, and `--no-deps` changed `com.docker.compose.depends_on` from `mysql:service_healthy:false` to empty. Read-only audit independently proved both differences; no apply repeat or automatic rollback occurred. Original intent/HOLD remains authoritative and is not overwritten as COMMITTED.
- Genuine authenticated Agent reconnects established node20/21 boot→installed durable state with same-process hashes and expected pins. Both actual Shadowrocket subscriptions exported with `insecure=0` and correct certificate fingerprints. Controlled public AnyTLS forwarding passed for legendjp and wawohksimple: authenticated HTTP200/body match and wrong-pin rejection before authentication; temporary listeners closed. Agents and independent GOST services were not restarted.
- Production receipts: `/root/network-panel-readiness-cutover-7d423770/postapply-readonly-audit.json` SHA `3d6d1842efc33e5f036a0e4f67061bca231e37211777ce9eba9b59dc8c82e38a`, `postapply-business.json` SHA `a7508d51bfd95939227202922a82acc32a211b21bb0892cd08752e838d60c65e`. Local final readback: `/workspace/network-panel-readiness-cutover/production-final-readback.json`.

Separate recovery ownership preparation is now complete. Reviewed tool SHA `2c466495806b2d8167add402b4c6b111fac71fa4922aa6145ed71408351162c8` passed 21 parent offline tests and seven independent B1-closure probes; earlier unchanged guards were inherited through frozen reviews. It authenticates the original precheck SHA `d8cc61801192bbfa842f8911c44675a3512884609516c47f802eae05b64ecf8e` and all 14 original receipt pins before observations and immediately before ownership recording. Docker environment names are checked as Docker entries, not shell identifiers; duplicate keys and changed values still fail closed. Native launch/result receipts contain no nonce and are authenticated by their actual byte hashes, not rewritten wrappers.

Production `recovery/recovery.py record` wrote only the exclusive private `recovery.owner.json`, SHA `55eed748fb0800463c91092929f61a32db84ff1ef4a124b3b10d80386a79fe83`, kind `READONLY_RECOVERY_NOT_ORIGINAL_COMMIT`. Exact-target readback via `owned()` passed, including all 37 historical file hashes, certificate/runtime/Compose/MySQL/image checks, HTTP gates, unchanged candidate ID and restart_count=0. Local readback is `/workspace/network-panel-readiness-cutover/recovery-final-readback.json`. No recovery rollback intent or verified receipt exists; no lifecycle action was executed during reconciliation. Recovery rollback preflight passes for the fixed current instance but actual rollback is unexecuted and needs a separate explicit operational decision; it never restores the DB automatically. The original script's owner/verified receipts remain absent and its HOLD is preserved. New rollback commands must use the separate recovery tool, not rerun original apply/rollback. Adding or changing files inside the frozen transaction evidence root intentionally invalidates future recovery checks; keep later notes/reports outside it.

No production diagnostic log deletion was performed; pruning survival was proven in the isolated MySQL/SQLite acceptance, not by deleting production audit evidence.
