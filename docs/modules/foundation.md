# 资产、绑定与持久电源任务

## 已实现的离线功能

同源管理界面提供用户、服务器、设备、绑定管理及任务／审计查询。管理员可创建和修改账户；密码由身份模块校验并哈希，更新用户会撤销其登录会话，禁止删除当前用户及降级最后一名管理员。客户只能看到自己的服务器；operator 也仅能读取归属自己的资产，不默认获得全局运维权限，不能提交控制动作。

服务器的 `notes` 可记录硬件、机柜、IP 和供电拓扑说明。设备可登记 `mock_pikvm`、`mock_pdu`、`mock_bmc`、PiKVM、APC、Redfish、IPMI 或 SNMP 类型。数据库只保存凭据引用，秘密配置不返回给前端或写入审计。设备地址录入不会触发网络连接。真实设备动作默认在任务入口和执行器两层禁用，启用设备记录不表示获准访问实机。

资产修改要求 `version`，陈旧版本返回 `STATE_STALE`。绑定使用独立历史行；替换时提交 `replace_id`、旧版本、同一服务器和用途，新行版本递增，旧行记录 `ended_at`。同一服务器仅有一个活动电源／控制台绑定；同设备同用途端口由数据库唯一索引保护。PiKVM 端口只能为 1–16；BMC 代表单台服务器，端口为 1；同一物理 PiKVM 端口的电源和控制台不能分给不同服务器。结束绑定不删除历史。已有关联历史的用户、设备、服务器由外键拒绝物理删除。

## API

列表直接返回数组，错误使用公共 `{error:{code,message},request_id}` 结构。所有写接口使用登录会话和 CSRF 校验。

| 方法与路径 | 内容 |
| --- | --- |
| GET/POST `/api/v1/servers` | 权限过滤列表／管理员新增 |
| GET/PUT/DELETE `/api/v1/servers/{id}` | 查询／版本编辑／删除无关联资产 |
| GET/POST `/api/v1/devices` | 管理员设备列表／新增 |
| PUT/DELETE `/api/v1/devices/{id}` | 版本编辑／删除无关联设备 |
| GET/POST `/api/v1/bindings` | 权限过滤历史／新增或原子替换 |
| DELETE `/api/v1/bindings/{id}` | 结束绑定，保留历史 |
| GET/POST `/api/v1/users` | 管理员列表／新增 |
| PUT/DELETE `/api/v1/users/{id}` | 修改／删除无关联用户 |
| GET `/api/v1/servers/{id}/power` | 缓存的电源／供电状态、时间及能力，不查询设备 |
| POST `/api/v1/servers/{id}/power-actions` | 持久化异步任务，必须有 `Idempotency-Key` |
| GET `/api/v1/tasks`、`/api/v1/tasks/{id}` | 权限过滤任务和结果 |
| DELETE `/api/v1/tasks/{id}` | 仅取消尚未执行的任务 |
| POST `/api/v1/tasks/{id}/resolve` | 管理员离线模拟结果核对，保留证据 |
| GET `/api/v1/audit` | 管理员全部审计，客户／operator 自己的操作审计 |

`POST power-actions` 仅接受 `{ "action": "power_on" }` 形式，不接受设备、端口、地址等目标字段。动作按设备能力提供：PiKVM／BMC 的 `power_on`、`request_shutdown`、`hard_reset`；PDU 的 `power_on`、`power_cycle`；模拟 BMC／Redfish 还提供 `power_cycle`，Redfish 执行前核对设备声明。PiKVM 管理员额外有 `enable_port`、`disable_port`。客户不能调用端口维护动作。IPMI 尚无核实命令能力，返回空动作列表。真实控制器的更细能力发现与接线条件须在实机授权阶段核对。

## 任务正确性与恢复

任务唯一键为操作者、服务器和幂等键；相同键和动作返回原任务，不重新入队；相同键不同动作返回冲突。排队时保存服务器、设备及绑定版本。领取任务时重新验证归属、角色、维护状态、WHMCS 服务状态、启用状态、绑定版本、设备版本、能力和端口；漂移任务失败且不会调用驱动。停服检查与生命周期操作共用服务器行锁；暂停／终止不会发物理断电。已终止的历史映射在新有效服务建立后不继续封锁新服务。

执行器用 PostgreSQL 会话 advisory lock 选出单个工作者；数据库任务行领取使用 `FOR UPDATE SKIP LOCKED`。设备 `lease_owner=task.id, lease_kind=power` 在调用驱动前提交，设备和服务器均串行。控制台共享该设备租约；任何控制台占用都会阻止提交电源动作。资产转移、换绑和设备编辑也会被未解决租约阻止。当前单工作者是保守实现，不承诺 HA 或跨设备并行吞吐。

任务终态区分 `succeeded`、`failed` 和 `unknown`。接受命令不等于完成；驱动即使返回成功但没有 `completed` 证据，也降为 `unknown`。PDU 只报告 `supply_state`，不由通电推导服务器启动。硬复位／供电循环不能从最终 ON 推导动作已经完成。

未知结果永久保留设备租约并设置 `blocked_reason`。服务恢复后，遗留 `running/verifying` 变为 `unknown`，不自动回到队列，也不释放租约。不会重发、不自动换驱动、不由超时推断旧执行者已停止。管理员只能对模拟设备用显式核验接口记录 `outcome`、`reason`、`executor_stopped=true` 后解除阻断；原结果保存在核验记录，审计记录核验人。真实设备的人工核验工作流需要明确授权后另行完成。

PiKVM 端口启用／禁用在设备级持久保存至少 30 秒冷却，即使驱动返回失败或未知也采用保守冷却；未知结果还同时阻断队列。前端网络失败后保留同一幂等键用于再次提交，不生成隐藏重发；新动作或明确成功响应后生成新的键。

## 验证与边界

单元测试覆盖能力、参数、接受与完成语义，以及授权窗口、精确目标、动作和凭据配置拒绝。PostgreSQL 集成测试覆盖跨用户访问、同端口冲突、版本历史、数据库唯一约束、并发幂等、设备串行、绑定／归属／设备漂移、未知结果恢复、维护冷却、停服检查与真实设备默认禁用。已执行的注入工厂集成测试只连接 `httptest` loopback PiKVM 夹具，验证一次命令、绑定生成端口、凭据服务端注入、撤销授权后拒绝已排队任务和结果脱敏。使用 `TEST_DATABASE_URL` 指向隔离开发 PostgreSQL，测试各自创建独立 schema；未提供时明确 `SKIP/NOT_RUN`，不能算数据库测试通过。

```powershell
$env:TEST_DATABASE_URL='postgres://barepulse@127.0.0.1:55432/barepulse_test?sslmode=disable'
go test ./backend/internal/app -run 'Test(Inventory|Binding|Power|RuntimePower)' -count=1
```

实际执行证据由主任务统一记入 `docs/implementation-status.md`。尚未进行任何真实设备 HTTP、SNMP、SSH、电源或端口操作。历史资产通过维护状态和结束绑定保留；首期不提供危险的级联删除。

## 受控真实驱动注册（当前未启用）

平台已把 PiKVM、Redfish 和 APC 驱动接入同一队列，不会绕过幂等、租约、版本复核和审计。只有 `ALLOW_REAL_DEVICES=true`、`ALLOW_DEVICE_WRITES=true` **同时**开启，且启动时载入的 `BAREPULSE_DEVICE_PROFILES_FILE` 中存在匹配设备 `credential_ref` 的完整批准记录，才能入队和执行真实动作。两道校验都要求授权尚未到期、证据说明非空，并精确匹配设备 ID、类型、端点、服务器 ID、端口和动作。配置读取、页面刷新和能力列表不探测设备。当前授权仅允许离线开发，交付配置保留关闭值。

下面仅说明文件结构，`approved:false`、过期窗口和占位凭据不能启用实机：

```json
{
  "lab-pikvm": {
    "device_id": "replace-with-enrolled-device-id",
    "kind": "pikvm",
    "endpoint": "https://192.0.2.1",
    "approved": false,
    "evidence": "PENDING: device, server, port, action and test window authorization",
    "authorization_expires_at": "2000-01-01T00:00:00Z",
    "allowed_server_ids": ["replace-with-server-id"],
    "allowed_ports": [1],
    "allowed_actions": ["power_on"],
    "username": "replace-from-secret-store",
    "password": "replace-from-secret-store",
    "mapping_verified": false
  }
}
```

秘密文件由部署端受限挂载到进程，只在内存使用，不提交 Git 或发送浏览器；JSON 不执行环境变量替换。HTTP 端点限制为登记的 HTTPS IP origin，不接受 URL 内认证、任意路径、查询或 DNS 名称。TLS 采用系统信任且不提供关闭证书校验开关。PiKVM 额外要求 `mapping_verified:true`。Redfish 要求 `redfish_system_path` 指向已确认的 `/redfish/v1/Systems/<id>`，执行时再次核对动作能力和系统身份，不选择发现结果的第一台机器。

APC 要求 `apc_profile` 包含 `confirmed/model/firmware/evidence/outlets`；每个插座包含核实的 `state_oid/control_oid/state_values/actions`。不内置任何猜测 OID 或控制枚举。`snmp_v3` 明确指定 `auth_protocol`（SHA/SHA224/SHA256/SHA384/SHA512）、`privacy_protocol`（AES/AES192/AES256）、两条口令及 `discovery_oid`；discovery 必须是该插座已确认的只读状态 OID。目标为登记的 IP 或 IP:端口，认证必须 AuthPriv。IPMI 保持明确不支持，不会自动替换为其它驱动重试。
