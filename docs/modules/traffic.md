# M2 流量与额度

实现入口：`backend/internal/app/traffic*.go`，迁移 `002_traffic.sql`，页面 `frontend/traffic.js`。

## 已实现的行为

- 一个账户有独立额度、不可变归属、1 台或多台同归属服务器成员。成员记录保存服务器版本；服务器换客户或版本漂移后阻断采集，历史归属和历史账本不重写。管理员可退役账户的来源和成员，再为当前归属创建账户。
- 来源分 `port` / `vlan` 与 `primary` / `observation`。端口来源的设备由有效网络绑定推导；客户不能选设备目标。VLAN 来源必须由管理员明确确认完整成员恰好等于账户成员。
- `network_domain + vlan_id` 表示同一网络流量范围。数据库触发器和事务锁阻止这个范围内 VLAN 与物理端口重复作为主源，包括跨账户并发创建；同一设备的稳定接口名、ifIndex、同一网络绑定也不能重复作为主源。已知 VLAN 端口含其它账户成员时拒绝整 VLAN 计量。辅助来源只供观察，永不入账。
- 以上拓扑是管理员声明并维护的模型；软件不能凭 VLAN ID 推断未知物理拓扑。真实部署前必须核实网络域、成员范围、接口名和上传/下载方向。错误地把同一范围登记为不同网络域会破坏该假设，不能把静态校验冒充物理发现。
- 来源身份、绑定版本和方向不可修改；换绑定、ifIndex 或接口名时退役重建来源。原始样本和账本按旧来源保留。
- 同一归属下正常修改服务器/绑定后，可在页面明确接受成员的新版本。该操作先退役受影响的端口来源和账户 VLAN 来源，再更新成员快照，要求新来源重建基线；原账户的额度、用量、调整和历史继续保留。换客户后不能用此操作恢复旧账户采集。
- 64 位原始计数器存 PostgreSQL `numeric`；差值和额度使用 Go `big.Int`；JSON 字节字段为十进制字符串。上传和下载分别保存，额度按 upload / download / both 配置计算。前端仅在绘制像素坐标时使用近似浮点值。
- 首次采样建立基线。复位、运行时间倒退、counter discontinuity、ifIndex/ifName 或绑定变化、可配置最大速率之外的跳变都不会被当成正常增量。未知区间的字节是 `null`，不写成零。
- 采集失败单独留样本。计数器连续的漏采恢复保留整个区间总量并标 `recovered_gap`；图表绘制区间平均值，不补造分钟数据。已检测到接口身份变化后恢复，必须新建基线，不能横跨该变化恢复计费。
- 跨账期且无法精确拆分的差值标 `boundary_unallocated`，保留完整双向增量但不计入任一周期；页面分别显示待核对量。没有默认均摊或补账政策。
- 每个来源按样本键和观测时间双重去重，样本内容冲突返回 409。迟到的非重放样本返回 409，不能暗改已形成账本。并发样本写入持有来源锁，账户级锁保证多来源汇总和告警一致。
- 周期支持 IANA 时区和每月第 1–31 天；短月取月末，下月恢复原始日期锚点，按本地日历处理 DST。基础额度不重写；增减额度通过带原因和幂等键的调整流水，不能使该周期额度为负。
- 告警按账户、周期、类型去重，页面可见。未接入外部通知渠道。自动收费、限速、断网、断电始终关闭；启用执法策略的请求被明确拒绝，不产生设备写操作。

## API

所有请求遵循统一认证和 CSRF 契约。除查询外均需管理员。

| 路由 | 用途 |
| --- | --- |
| `GET /api/v1/traffic/accounts` | 按角色和客户归属列账户 |
| `POST /api/v1/traffic/accounts` | 名称、owner_id、server_ids、quota_bytes、direction、cycle_day、timezone、alert_percent |
| `GET /api/v1/traffic/accounts/{id}?at=<RFC3339>` | 所选周期的精确用量、额度、待核对量、成员、来源、区间、最新样本质量和告警 |
| `GET /api/v1/servers/{id}/traffic` | 校验服务器归属与当前成员版本后读取账户详情，供客户和 WHMCS 使用 |
| `DELETE /api/v1/traffic/accounts/{id}` | 退役所有来源和成员，保留历史账户、样本、账本、调整和审计 |
| `POST /api/v1/traffic/accounts/{id}/members/{server_id}/refresh` | 接受同一归属下的成员新版本，退役受影响来源并要求重建基线 |
| `POST /api/v1/traffic/accounts/{id}/adjustments` | bytes（可负）、reason、period_start、idempotency_key |
| `POST /api/v1/traffic/sources` | 创建来源；端口使用 binding_id，VLAN 使用 device_id + 完整 vlan_member_ids + coverage_confirmed |
| `DELETE /api/v1/traffic/sources/{id}` | 退役来源 |
| `POST /api/v1/traffic/sources/{id}/samples` | 仅模拟来源允许手动原始样本输入，用于可重复离线验收 |
| `POST /api/v1/traffic/sources/{id}/collect` | 执行一次真实持久化的模拟/只读 SNMP 采集，body 为 `{ "scenario": "normal" }` |

周期查询中的 RFC3339 字符串必须 URL 编码，建议用 UTC 的 `Z` 表示。客户响应清除设备、绑定、管理接口、ifIndex、网络域与 VLAN 标识，账户成员只列当前仍归属于同一客户的资产。跨客户账户查询返回 404。

当前图表返回最多 2000 个区间，表格展示最近 100 条；汇总 SQL 覆盖整个周期，不受图表条数限制。`known_intervals`、`unknown_intervals`、`coverage_note` 明确说明用量仅含已确认区间，不能将未覆盖时间理解成零流量。

## 离线页面工作流

1. 在资产页创建同一客户的一个或多个服务器，创建模拟设备，再为服务器建立 `network` 端口绑定。
2. 进入“流量与额度”，选择客户和成员，填写明确的字节额度与周期，创建账户。按 Ctrl / Command 可多选同客户成员形成共享额度。
3. 给当前账户添加 `port` 主来源；选择网络绑定，填写示例网络域、VLAN、稳定接口名和 ifIndex。保持“模拟来源”开启。可以启用后台自动采集，默认周期 60 秒。
4. 点击“运行模拟采集”：第一次写入基线，后续调用根据真实经过时间生成累计计数器并写入差值。模拟数据明确标识，初始计数器刻意大于 JavaScript 安全整数范围，以检验精确处理。
5. 可切换 offline / reset / reboot / discontinuity / remap 场景，查看持久化样本质量、区间和告警。恢复正常不会把失败当作零，也不会跨接口身份变化误入账。
6. 新增同域同 VLAN 的主 VLAN 来源会被拒绝；将其设为 observation 可观察，但不会累加进额度。VLAN 来源必须先确认完整成员。
7. 添加带原因的额度调整，查看本期额度和审计。退役来源或账户后历史仍可查询。

## 只读 SNMP 适配

`SNMPTrafficCollector` 实现 `TrafficCollector`，使用固定的 IF-MIB 和 sysUpTime 只读对象：ifName、ifHCInOctets、ifHCOutOctets、ifCounterDiscontinuityTime、ifAlias、ifOperStatus、sysUpTime。`Discover` 只读遍历 ifName，返回接口名/索引供授权接入时核对，不自动改写现有来源。

对象定义依据 [IETF RFC 2863](https://www.rfc-editor.org/rfc/rfc2863.html) 与 [IETF RFC 3418](https://www.rfc-editor.org/rfc/rfc3418.html)。这些标准对象不是 SN2410 Onyx 3.9.3202 或 VyOS 1.5 的实机支持证明。解析器要求 Counter64 和完整响应，不自动降为 Counter32，不猜设备私有 OID。

默认 `ALLOW_REAL_DEVICES=false`。关闭时，采集和发现都在创建网络连接之前拒绝；后台只调度模拟来源。真实来源还需要登记为启用的 `snmp` 设备、显式关闭来源 mock 标记以及有效凭据引用。

设备 `credential_ref` 指定一个环境变量名，变量值为 JSON：

```json
{"username":"READ_ONLY_ACCOUNT","auth_password":"FROM_SECRET_STORE","privacy_password":"FROM_SECRET_STORE","context_name":""}
```

该凭据结构仅演示字段，不能直接用作生产凭据。只支持 SNMPv3 AuthPriv，SHA-256 + AES；其它算法未默默降级。连接用设备登记的地址（主机或主机:端口），3 秒请求超时、0 次自动重试、10 秒整体截止时间，无 SET 调用。凭据不进数据库、响应、审计或错误消息。后台每 5 秒找应采来源，各来源间隔可配置为 10–3600 秒，并用 PostgreSQL advisory lock 防止多实例重复轮询。

## 验证证据与边界

2026-10-15 已实际执行并通过：

```powershell
$env:TEST_DATABASE_URL='postgres://barepulse@127.0.0.1:55432/barepulse_test?sslmode=disable'
..\toolchain\go\bin\go.exe test ./backend/internal/app -run '^TestTraffic' -count=1 -v
node --check frontend/traffic.js
node --input-type=module -e "import('./frontend/traffic.js').then(()=>console.log('traffic ES module load PASS'))"
```

上面的工具链路径是本次工作区的临时工具链位置，常规开发使用 `go test`；测试不配置 TEST_DATABASE_URL 会明确标记 `NOT_RUN` 并跳过 PostgreSQL 测试。当前运行时的 `node --check` 曾漏报模块语法错误，因此额外实际加载 ES module，不能只依赖该语法检查的退出码。

覆盖：超过 2^53 的差值、Counter64 上限、方向映射、未知数据、首次基线、复位、重启、计数器中断、映射变动、绑定漂移、漏采恢复、跨周期保留、月末和 DST、共享端口汇总、观测不入账、12 个并发重放仅一条账本、端口/VLAN 并发冲突、客户越权、服务器客户入口、额度调整重放及冲突、告警去重、模拟采集持久化、真实采集/发现默认禁用。真实 PostgreSQL 的独立 schema 和迁移均实际执行，未用内存库替代。

实机验证为 `BLOCKED`：未获 SNMP 访问授权，未验证设备支持对象、采集方向、缓存刷新周期、真实端口/VLAN 拓扑和计量口径。没有执行真实设备 HTTP/SNMP/SSH。SNMP 解析和默认关闭契约测试通过不代表设备联调通过。页面语法检查通过；浏览器完整操作验收由主任务集成阶段记录。
