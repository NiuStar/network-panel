# Exit editor ownership hotfix

## Scope

The exit dialog shared common fields across SS and AnyTLS, accepted stale read responses, and initialized to SS when its loader identity changed. Saved configuration and running listeners are separate authorities: node15's saved SS remained37170, while its independently saved/running AnyTLS was10086. No SS37170 runtime recovery is included in this release.

Both maintained frontend variants now fence loads by session, node ID, protocol and request generation. Only successfully loaded current owners can save. A modal-instance pending-write lock remains held until actual settlement, independent of read reloads, protocol switching or close/reopen. Obsolete responses cannot close/toast/update the next dialog. This is not cross-tab or server-side write serialization.

## Validation

- Identical source-extracted requirement contract: baseline6/28, successor28/28.
- Actual React18.3.1/test-renderer deferred-API checks: pending-write36, independent immediate duplicates36, fresh settlement12, original28;112 checks passed.
- Both final sources executed `tsc && vite build` successfully on Node24.21.0. Repository Docker usesNode22; toolchain difference retained. npm follows repository Docker's existing `--legacy-peer-deps`. No dependency declarations or generated lockfiles are changed by this commit.
- Desktop Chromium loaded the full final V2 application with local intercepted fixture APIs:8/8 PASS, zero page errors. Read race, preview race, close/reopen, failed read/save disabled, positive exact AnyTLS payload, pending save/no duplicates, save error and certificate-feature delay passed. Full dist manifests matched before/after/current files. This is not native mobile, paired baseline-browser or production backend acceptance. Screenshots lacked Chinese glyphs; DOM assertions used actual labels.
- Browser report SHA `9cf6a2a4df83a873f95bfe2ab74ca98a65eeb6aeff3b57cc893ca23c1c459730`; V2 node asset SHA `01d6d7c135b0b73005e1a59158ef2c2f344c5a851ef8de514ff5ff7be772a2e9`.
- V1 source SHA `010b919fca80662adaedbafb03f1d33c6ee6de5c96c1c2f95fe66b8e4f677449`; V2 source SHA `6a22ee8b12ab79d9f80beb0e011d5f9b555d45680a4d2aa9a917f4dc4a35b5e0`.

## Release boundary

Publish an amd64 frontend-only immutable successor over production base `24802117/network-panel@sha256:caf3c07843b12308749903babbc35fe46331b182b43e9391f1f84b04744988b1`. Replace only UI payload under `/app/public`, preserving inherited `flux-agent` and any other non-UI files. Hash complete base/candidate inventories and retain unchanged `/app/server`, launcher, installation scripts and downloadable Agents. Do not move `.32` or latest tags and do not imply a new backend/Agent release.

Keep certificate automatic enrollment (`ANYTLS_CERT_NODE_IDS=""`), enabled DB feature and `AGENT_AUTO_UPGRADE=false`; keep production16365, network, mounts, MySQL and existing certificates/settings. Use an independent database/port candidate before a single authorized Compose backend image replacement; do not restart Agents/GOST or replay exit setters. Retain online backups and old image/config for explicit recovery, never automatic DB restoration. Publication, isolated image QA and production activation must each be reported separately.

Evidence workspace: `/workspace/anytls-exit-modal-fix-20261116/`; tests and historical rejected candidate receipts remain there. No secrets/raw production DB are committed.
