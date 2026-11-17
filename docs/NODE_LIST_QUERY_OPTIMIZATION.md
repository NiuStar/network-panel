# Node list query optimization

The authenticated node list was slow even on loopback:5.05–24.43s; public6.24s. Anonymous401was0.19s. Production MySQL5.7.44 node_op_log had~230krows/~90MB and only primarykey; node_anytls_cert_log also lacked lookup indexes. Four list queries performed fullscans; one empty bind-error history query took16.7s.

## Immediate production relief, separately completed

A private snapshot clone proved unchanged query result digests and index plans. Qualified additive indexes:
- node_op_log:idx_node_op_node_time(node_id,time_ms,id),idx_node_op_cmd_node_time(cmd(64),node_id,time_ms,id)
- node_anytls_cert_log:idx_cert_node_time(node_id,time_ms,id),idx_cert_node_step_time(node_id,step(64),time_ms,id)

Online production ALTER usedALGORITHM=INPLACE,LOCK=NONE,metadatawait5s,freshsingletransactionbackup,1.5GBminimumdiskgate. Build~5s; no restart/deletion. Readbacklistloopback0.927/0.776s,public0.840s,22nodes/code0. Observed samples, not latencySLA. Privateevidence/root/node-list-index-production-20261116; clone/root/node-list-query-qa-20261116.

## Backend candidate

latestNodeBindLogs selects one matching full row per visible node using a MySQL5.7/SQLite correlated ID lookup orderedtime_msDESC,idDESC. Selection precedes parsing; newestNULL/invalid stdout does not resurrect old data. Success-category precedence/errorfallback retained. Runtime counts and TLS diagnostics retain their entire900-secondwindow. Certificate/readiness/pin code unchanged. Atmostone returnedrow is not a constant scan-cost guarantee when message filtering must inspect older rows.

Startup additive-index helper verifies exact orderedcolumns/MySQLprefixes, preflights allnamesbeforechanges, pinsconnection forsessionlock_wait_timeout save/set5/restore, givesDDL30scontext, refusesCOPY/blockingfallback. Exactvalidexisting indexes metadata-only. Duplicate-name race accepted onlyafterdefinitionverification. SQLitepartial-indexmetadata is not checked: independentreview reproduced weakerperformancecoverage; querycorrectness is not bypassed. UNIQUEalone is not writeblockerbecauseeveryindexincludesuniquePKid. Retainthisnonblockinglimitation.

## Qualification

- SQLite realGinfullJSON/fullrow/ties/NULL/errorfallback/visibility/runtimecounts>1000 tests; semanticbaselineoverlayfailsintendedtie/latestNULLassertion; candidatepasses.
- Full go test ./golang-backend/... and affectedvet passed onGo1.27.4.
- ActualMySQL5.7 freshprivateDB:bothTestNodeListLatestBindBoundedFullRows,TestNodeListJSONLatestInvalidRuntimeAndVisibility passed. Test-only openeroverlayexplicit; productionfunctionunchanged.
- ActualMySQL5.7 TestMySQLNodeListIndexStartup passed:create/repeatunchangedschema/wrongprefixrefusal/rowpreservation/sessionrestore.
- IndependentfrozenfivefilereviewPASS_WITH_NONBLOCKING_METADATA_GAP; sourcefreezeandindependentcontroller/db/panelupgrade testspassed. No productionactionbyreviewer.

Evidence/workspace/node-list-optimization-evidence/. Publicationandproductionbackendactivationremainseparategates. Preserve deployedfrontendbe4a0dcbytes, all-nodecertificateenrollment,AGENT_AUTO_UPGRADE=false,ports,mounts,CA/leaf/settings. NoAgent/GOSTrestartorSSrestoration included.
