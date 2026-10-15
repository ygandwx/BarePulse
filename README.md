# BarePulse

轻量级物理服务器管理平台，统一服务器资产、流量额度、电源操作和远程控制台，并向 WHMCS 提供标准 API。

已进入首期实现：Go + PostgreSQL 后端、同源管理界面、模拟电源任务、流量账本、共享控制台和 WHMCS 模块。代码与离线验证进度见 [实现状态](docs/implementation-status.md)。真实设备访问默认关闭；模拟验证不代表实机验收。

## 本地启动

依赖：Go 1.27.2、PostgreSQL 18.6；前端使用浏览器原生 ES modules，无 npm 构建。Docker Compose 可管理本地数据库与应用。

```sh
cp .env.example .env
# 编辑 .env，设置唯一数据库密码和 12–72 字节管理员口令（示例不能直接用于生产）。
docker compose up -d db
docker compose build app
docker compose run --rm app migrate
docker compose run --rm app bootstrap
docker compose up -d app
```

打开 `http://127.0.0.1:8080`，用初始化账号登录。初始化只允许空用户库；不要重复运行来重置账号。执行后从 `.env` 移除 `ADMIN_PASSWORD`，已运行应用不需要管理员明文口令。

无 Docker 时，先建立本地空库并设置 `DATABASE_URL`、`ADMIN_EMAIL`、`ADMIN_PASSWORD`：

```sh
go run ./backend/cmd/barepulse migrate
go run ./backend/cmd/barepulse bootstrap
go run ./backend/cmd/barepulse serve
```

程序不会自动加载 `.env`；原生运行需用当前 shell 设置环境变量。默认监听 `127.0.0.1:8080`。首轮操作：新增客户 → 新增服务器 → 新增 `mock_pikvm`／`mock_pdu`／`mock_bmc` 设备 → 电源绑定 → 电源任务页面提交动作 → 查看任务结果及审计。模拟数据清楚标记，未采样状态为 unknown。

## 验证

使用专用测试数据库，测试自动创建并清理独立 schema：

```sh
export TEST_DATABASE_URL='postgres://barepulse:your-test-password@127.0.0.1:5432/barepulse_test?sslmode=disable'
sh scripts/verify.sh
```

Windows：设置同名环境变量后执行 `./scripts/verify.ps1`。Linux 检查包含 race detector；Windows 当前测试环境无 CGO，普通并发和真实数据库测试可执行，race 单独记录未运行。缺少 `TEST_DATABASE_URL` 时统一验证入口会失败，不把跳过集成测试当作通过。

API 契约执行 `python3 api/check_contract.py`（Windows 用已安装的 Python 路径）。WHMCS 安装与 PHP 离线测试见 [WHMCS 模块](docs/modules/whmcs.md)；运行 `scripts/verify-whmcs.ps1` 或 `sh scripts/verify-whmcs.sh`，PHP/cURL 可用时 Go 测试还会实际调用跨语言 HTTP 契约。Docker 可用时 `sh scripts/compose-smoke.sh` 在独立项目中跑启动到任务审计首链。运行配置、[备份恢复](docs/backup-restore.md)和现场验收限制见 [运维手册](docs/operations.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [开发流程与架构图](docs/development-workflow.md) | 系统架构、阶段依赖、持续实现循环、具体任务与验收门槛 |
| [Codex 编码规则](AGENTS.md) | 持续推进、设备边界、测试、提交与暂停条件 |
| [实现进度与续接记录](docs/implementation-status.md) | 阶段状态、测试证据、阻塞与下一条任务 |
| [离线验收矩阵](docs/offline-acceptance.md) | A01–A16 对应代码、实际测试和实机边界 |
| [OpenAPI 3.1](api/openapi.yaml) | 当前 HTTP 接口、认证与错误契约 |
| [Codex 启动提示词](docs/codex-start-prompt.md) | 可复制的持续编码任务指令 |
| [开发计划](docs/development-plan.md) | 已确认需求、约 30 台服务器的设备基线、流量核算、电源驱动、共享控制台、数据模型、API、WHMCS 对接、开发阶段与验收标准 |
| [定制 16 口 PiKVM 接入细节](docs/pikvm-integration.md) | 手册与 kvmd 3.142 源码排查、接口映射、返回值、鉴权、Nginx 路由、共享媒体隔离及待验证项 |

## 首期接入目标

- Mellanox SN2410 / Onyx：服务器交换机端口流量。
- VyOS：VLAN 接口流量与汇总核对。
- APC AP7921B：绑定插座的供电控制。
- 定制 16 口 PiKVM：逐端口电源控制，以及整台设备独占的远程控制台。
- TYAN IPMI / Dell iDRAC：按实际 BMC 能力接入。
- WHMCS：服务绑定、流量和状态展示、电源任务、控制台入口。

## 当前边界

已完成 PiKVM 源码、上传手册和路由配置的静态分析。尚未进行实际 API 验证、端口切换、电源控制、SNMP 写操作或网络配置变更。实机操作必须使用另行明确授权的设备、端口与测试窗口。

已确认要求、建议实现方案与待验证行为在文档中分别标记。具体设备地址、口令、客户资料和原始运维日志不纳入仓库。
