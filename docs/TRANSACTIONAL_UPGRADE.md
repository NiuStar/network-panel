# 面板在线事务升级

本文描述本分支的升级契约与发布准备，不表示任何版本、资产或部署已经发布或验收。

## 支持边界与首次安装

- **Docker/容器拒绝在线覆盖二进制**；只能通过外部镜像升级流程更新，不能把容器内文件修改当作镜像升级。
- 原生在线升级仅支持 root 管理的 Linux 标准安装：`/opt/network-panel`、`/usr/local/bin/network-panel-server`、systemd `network-panel.service`，以及本机 SQLite。原生事务架构为 `amd64`、`arm64`、`armv7`；其他发布平台资产仍保留，但不因此获得在线升级支持。
- SQLite 必须位于受支持安装目录内的固定普通文件路径，停止服务及数据库写入者后保留数据库和 WAL/SHM 的升级前镜像。`.env`、证书与其他本地秘密不得混入发布包。
- MySQL 尚无完整数据库回滚链，默认拒绝在线事务升级；不要通过只回退程序来声称完成数据库回滚。
- 新进程在初始化数据库前核对事务绑定的 SQLite 路径、dialect、端口与配置摘要；初始化后使用真实 SQLite `PRAGMA database_list` 路径生成 PID/版本/程序 SHA 就绪回执。提交前节点连接和定时任务保持维护闸门，避免本地回滚前向外部节点下发变更。
- 支持可选 `proxyPrefix` 下载加速，但 GitHub API 的 release 身份与 digest 必须直连读取；代理不能成为其自身下载字节的唯一校验 authority。
- **旧版本没有新事务引擎，不能靠旧在线升级入口自举。** 必须先另行发布支持该引擎的新版本，并通过外部安装/升级流程安装它，才可用该引擎升级下一版本。本次代码与工作流准备不等于已完成这一步。

## 同一固定 tag 的发布契约

事务固定一个明确 tag，不在执行中重新选择 `latest`，也不混用分支、源代码 raw 链接或其他 tag 的文件。使用 GitHub release asset 元数据中的 `sha256:` digest 校验下载字节，并与同一 release 的 `SHA256SUMS` 交叉核验；缺失、重复、身份或校验不符均应拒绝激活。

所需发布内容包括：

- 对应原生架构的 `network-panel-server-linux-<arch>`；
- `frontend-dist.zip`；
- `panel-runtime-scripts.zip`，含根 `install.sh` 与 `easytier/` 的全部受 Git 跟踪静态文件；
- 独立 `install.sh`，继续供兼容安装流程使用，字节必须与 ZIP 内的 `install.sh` 一致；事务引擎使用 ZIP 内的版本；
- 六份 Linux 主架构 agent 资产：`flux-agent-linux-{amd64,arm64,armv7}` 与 `flux-agent2-linux-{amd64,arm64,armv7}`，构成原生事务 `public/flux-agent/` 的下载内容；
- `BUILD-INFO.json` 与 `SHA256SUMS`。

`BUILD-INFO.json` 保持 `{ "tag": "固定标签", "commit": "40 位小写十六进制 SHA" }` 格式。GitHub Release 的 `target_commitish` 必须是同一完整 SHA，不能是默认分支名；工作流创建 release 时显式传入 `--target "$RELEASE_SHA"`，其中 `RELEASE_SHA` 来自 `${{ github.sha }}`，与 BUILD-INFO 的 commit 来源一致。

`.github/workflows/release.yml` 仍只由 `v2.*` tag 推送触发，维持原有 **8 个 server + 48 个 agent** 二进制发布矩阵及 56 份二进制计数检查；ZIP 是额外资产，在生成 checksums 之前打包。只修改工作流不会运行或发布它；本次不创建 tag、不发布、不部署、不重启。

## 可复现脚本包

在固定 tag 的干净 checkout 中运行：

```sh
python3 scripts/test_package_runtime_scripts.py
python3 scripts/package_runtime_scripts.py --output release-assets/panel-runtime-scripts.zip
```

打包器使用 Git index 枚举 `install.sh` 和 `easytier/`，读取相应工作树字节，不扫描或收录 untracked 文件。受跟踪的 `.env` 类文件、数据库/SQL 文件及其 sidecar、符号链接、Git 子模块、特殊文件或不安全路径会使打包失败；文件缺失也不会被静默跳过。发布必须使用干净 checkout，不能把本地修改冒充 tag 内容。

ZIP 成员均为相对路径的普通文件，按路径排序，无目录成员；时间固定为 `1980-01-01 00:00:00`，Unix 权限为 `install.sh` / `easytier/**/*.sh` 的 `0755`、其余文件 `0644`，无额外字段/注释，使用 `ZIP_STORED` 避免压缩库版本差异。相同文件字节不受 mtime、工作树权限或 untracked 文件影响。仅完整写入的 ZIP 会原子替换输出，失败保留已有资产。

## 实施验证边界

候选验证覆盖下载/解压的失败关闭、跨进程互斥、激活与验证阶段被杀后的持久状态与显式恢复、真实 ELF 子进程切换/回旧，以及真实 SQLite WAL/DDL 写入失败后的完整性、结构及记录恢复。两套前端均验证只有 COMMITTED 才成功，并完成生产构建。

上述进程验收使用临时目录和测试拥有的子进程/回调，不调用真实生产 systemctl。原生标准安装的 systemd-run 派发、服务停止/重启及下一正式 Release 的实下载升级尚需在独立原生 Linux 验收环境演练；这不是生产升级已完成或所有故障均可自动恢复的证明。断电等无法确定的状态保留材料、阻止新升级并要求人工复核。

## 状态与故障处理

- 管理员通过 `POST /api/v1/version/upgrade` 提交 JSON 中的固定 `tag`，例如 `{"tag":"v2.0.0.31"}`（仅格式示例，不表示该版本已发布）。返回事务 ID/已受理不表示升级成功。
- 使用 `GET /api/v1/version/upgrade-status?id=<事务ID>` 读取该事务。日志/SSE 只是观察通道，断开、重连、超时或服务短暂不可用均不是成功，也不应触发另一笔升级。
- **只有 `COMMITTED` 表示成功**：新进程、新程序 SHA/版本及健康校验必须完成。`FAILED`、`ROLLED_BACK` 都不是升级成功；`ROLLBACK_FAILED` / `RECOVERY_REQUIRED` 要求人工处理。
- 原生独立 worker 保留 `/opt/network-panel/.upgrades/<事务ID>/` 的状态、下载/staging 和数据库 before-image，以及正式目标旁的事务备份。保留并限制访问这些材料，其中可能包含环境或数据库秘密。
- 遇非 terminal 崩溃或无法确认的 worker 退出，应按 `RECOVERY_REQUIRED` 人工复核处理，禁止盲重试、绕过锁或删除事务目录。人工先核对事务状态、worker/systemd 状态、运行程序 SHA/版本、健康与完整备份，再决定恢复；没有可验证的完整数据库回滚链时不得强行继续。
