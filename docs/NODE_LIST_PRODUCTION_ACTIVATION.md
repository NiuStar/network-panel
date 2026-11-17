# Node-list optimization production activation

Implementation `97c97a8d7bc8fb3fbf8a8dadc6a8cac287caaaa6`, branch `fix/node-list-query-bounds`, remote ref verified. Independent five-file review matched committed bytes; PASS with nonblocking SQLitepartial-index metadata gap retained. Full backend tests, affected vet, actualMySQL5.7 query/fullGin and startup-index tests passed.

Clean VCS build: Go1.27.4/linuxamd64/CGO0, vcs.modified=false, serverSHA `fb328733f561495fb6117f986f7d1d48a99fdae73a70fe093319726223ed6756`. Historical same-source candidate in a prior remote directory had different bytes; it was not overwritten or adopted. An interrupted uncompressed upload was retained, gzip successor transfer verified before building.

Immutable image: `24802117/network-panel:node-list-97c97a8-fb328733@sha256:79d441f7bceedd8741d8f1a444cc99f88488b12f30143df5eedd698852aec4a2`. Published/pulled back and actualimageID checked. Built over the exact previously deployed frontendhotfix base;82appfiles compared, onlyserverchanged. All69UIassets includingnodebundleSHA01d6d7c135b0b73005e1a59158ef2c2f344c5a851ef8de514ff5ff7be772a2e9 preserved. No .32/latest tag promotion or Agentpayloadreplacement.

IsolatedQAusedprivateMySQLclone, account and nodedisabledsecrets, unpublishedhostport. Anonymous401/full69assetHTTP, candidateactualserverSHA and22nodelist passed. First request19.576s retained; second1.112s. Later same-clone serial samples:baseline.339/.307s,candidate.446/.403s. Those samples do not prove cold-cache cause or constantlatency; residualmessagefiltering remains. No arbitrarydeadlineincreaseorlogdeletion.

Production additiveindexes had already removed the main fullscanbottleneck withoutrestart. The later authorized backend switch replacedComposeimageonce afterfreshonlineDBbackup. Container `16554b0513bfdb1e9914e0d9c08cae7abba730461f9f45a49311f432e073d8db`, restart_count0. Initial local2.164s, public.879s, subsequentlocal.723s;22nodes/businesscode0. No long-term SLA claimed.

MySQL ID, ports16365, mounts, full runtimecontract, certificateCA/existingleaves, exitrows/credentials/ports and all-nodeautomaticcertificateenrollment preserved. Agentauto-upgradefalse. No Agent/GOST restart, exitsetter replay or SS37170restoration. Legendjp/wawohksimpleactualstrictpinnedsubscriptionexports passedfinalreadback. QAcontainer stopped.

Production backup/intent/owner/result: `/root/node-list-release-97c97a8-fb328733/`. Read-back productionresultSHA `54cc5951cdbf5365a8823a8e54b1aff191a0e653948196511013a2d494b0b821`; local `/workspace/node-list-optimization-evidence/production-final-readback.json`. Retainedonlineindexesremainafterimage rollback; database restore is not automatic. No live rollback executed.
