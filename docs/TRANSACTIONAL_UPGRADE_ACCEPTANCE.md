# 事务在线升级候选验收记录

本记录只针对功能分支候选，不表示版本已发布、原生 systemd 已演练或生产已部署。

## 已执行

- 旧接口回归 RED：升级失败时仍返回 code=0；SSE 在 error 后发送 done。
- 修复后 HTTP 进程验收：新临时 SQLite、独立端口及候选程序；匿名升级401、管理员 Docker 升级409、无ID流400、路径穿越状态请求400；正式安装脚本字节不变，不创建事务，测试进程停止。
- Go 全套 `go test ./golang-backend/...`、`go vet ./golang-backend/...`；包含真实 ELF 子进程切换/失败恢复、真实 SQLite WAL/DDL回滚、跨进程 flock 和激活/验证阶段 SIGKILL 后阻止新事务。
- 两套前端生产构建通过，测试源码与构建机对应文件SHA一致；68项Node测试在限CPU条件下连续三轮通过。修正仅测试夹具40ms不稳定截止，生产截止和提交成功语义未放宽。
- 运行时脚本包14项测试通过；原56份二进制平台矩阵不变；actionlint及Swagger JSON/YAML schema校验通过。

## 失败证据与收尾

最终独立复核发现 SQLite DELETE 模式 hot rollback journal 未备份可导致回滚后的逻辑数据损失，旧候选未放行。修复已将 `-journal` 纳入路径校验、固定目标、备份、before-image 校验、恢复与缺失记录失败关闭，并通过真实 DELETE 写入者被杀、candidate 消费 journal 后提交 DDL、再失败回滚的业务断言回归。缺少 `db-journal` 证据的旧持久记录不能推断为文件原本不存在。修复后的目标源码 SHA 经独立复核绑定验证，专项测试与复核均通过。

最终后端全套执行记录为 386 个通过的测试/子测试事件，其中事务引擎 106 个，失败为 0；`go vet` 通过。该计数不包含两套前端的 68 项 Node 测试和 Python 打包/安装脚本测试。

## 未执行的门禁

- 独立原生 Linux 标准安装上的真实 systemd-run 派发、完整 service stop/start、正式 Release 下载升级与故障恢复演练。
- 生产数据库迁移/恢复、生产面板/节点重启、Docker 发布、main 合并。

原生升级不可只凭这些离线/候选测试视为可直接投产。Docker 在线升级明确拒绝容器内二进制修改；MySQL 默认拒绝自动事务升级。
