# 外部依赖与安全取证清单

核验日期：**2026-10-19**。范围：SN2410、VyOS、APC AP7921B、定制 16 口 PiKVM、TYAN IPMI／Dell iDRAC、WHMCS。本文列出尚缺的材料和现场条件，不重新安排已经完成的首期实现。代码、离线检查和现场结果分别以[实现记录](implementation-status.md)、[离线验收矩阵](offline-acceptance.md)及后续现场记录为准。

**当前没有执行真实设备 HTTP、SNMP、SSH、电源、端口切换或虚拟介质操作。** 下列命令和路径仅供拥有相应权限的管理员以后按批准范围取证，不构成访问或控制授权。不要把资料收集与开启 `ALLOW_REAL_DEVICES`、`ALLOW_DEVICE_WRITES` 合并操作；本轮开发继续保持两项为 `false`。

## 如何使用

每个编号只在其对应功能接入时关闭。提供资料时引用编号、版本和脱敏摘要即可；不必一次交齐，也不要把口令、Cookie、Token、SNMP 密钥、完整配置、客户资料、私有镜像或原始抓包放进聊天或仓库。

| 级别 | 需要的时点 | 当前影响 |
| --- | --- | --- |
| 现在必须提供（限定功能） | 要继续实现某个尚无协议依据的定制适配之前 | 仅 KV-01～KV-04 的固件静态协议材料；可先交已有本地副本，不需要设备访问授权或秘密。缺一项只阻塞对应真实通道适配，不阻塞其它离线工作。 |
| 实机联调前 | 决定接入该设备、该功能，准备申请具体只读／控制窗口时 | 型号、能力、绑定、脱敏样本及受限环境。没有这些资料仍可做模拟、契约、前端和故障恢复测试。 |
| 上线前 | 对真实客户开放对应功能之前 | 真实隔离、动作语义、计量口径、宿主兼容性和恢复证据。材料齐全不自动等于验收通过。 |

共同取证规则：管理员在本地查看原文，再按条目白名单摘取字段；保留字段类型、状态码、错误层级、路径改写和参数规则，敏感值改成一致的别名。不要上传 `env`／`printenv`、`phpinfo()`、完整 `show configuration`、完整 `nginx -T`、未处理 HAR 或调试日志。账户名、管理 IP、域名、序列号、MAC、客户／服务编号也用稳定别名替换；密码、口令哈希、认证头、会话和密钥直接删除。脱敏不能把端口编号关系、JSON 类型、正则或查询参数结构一并抹掉。

设备授权另行记录精确设备、服务器、插座／端口、允许动作、执行人及起止时间。只读授权不含 SET、POST 控制、服务重载、拔线、重启或模拟故障。实际凭据由部署者通过受保护配置／秘密存储注入；此处只记录引用名称和权限是否满足，见[运行与恢复](operations.md)。

## SN2410／Onyx

已有基线为 SN2410、Onyx **3.9.3202**、BIOS 4.6.5，见[设备基线](development-plan.md)。[流量模块](modules/traffic.md)和[采集实现](../backend/internal/app/traffic_collectors.go)已有 IF-MIB 只读采集、身份复核、精确计数器和质量处理。本轮代码按[算法解析器](../backend/internal/drivers/snmp.go)显式选择 SNMPv3 AuthPriv 算法，不自动降级；`SHA` 与 `SHA256` 不是同一个配置值，AES 扩展也不能只凭名称猜兼容。

官方可核对到的[Onyx 网络管理命令参考](https://docs.nvidia.com/networking/display/nvidiaonyxusermanualv3104408lts/network+management+interface+commands)为 **3.10.4408 LTS**，不是目标 3.9.3202 的适配证明。下述 `show snmp` 系列命令必须先在本机帮助或同版本手册确认；本清单未获得 3.9.3202 对应主源命令页。

### SN-01 — 当前版本与只读 SNMP 能力（实机联调前）

- **缺少／影响：** 当前固件是否仍为 3.9.3202、SNMPv3 已启用的认证／隐私算法、context、只读 view 范围、采集源地址 ACL 摘要。决定能否建立受支持的 AuthPriv 会话；不要求立即开通账号或修改配置。
- **已有：** [计划中的型号记录](development-plan.md)、[流量凭据结构](modules/traffic.md)、[采集会话建立代码](../backend/internal/app/traffic_collectors.go)。
- **安全获取：** 管理员从当前版本页／设备台账摘录型号与软件版本；版本确认后，可在本地查看 `show snmp`、`show snmp user`、`show snmp set-permission`，只交算法、只读权限、view 是否覆盖下列对象和 ACL 来源别名。3.9.3202 命令未核对时先用本机帮助，不照抄新版配置命令。删除 community、账号实值、engineID、联系信息和所有密钥／哈希。
- **模拟边界：** 可验证算法拒绝、超时和解析；不能证明该固件、账号、ACL 和实际密码组合可用。若算法不匹配，先记录精确名称并离线适配，不能自动退回 v2c、NoAuth 或明文模式。

### SN-02 — 服务器到交换机接口的绑定表（实机联调前）

- **缺少／影响：** 已选试点服务器别名、物理端口、稳定 `ifName`、当前 `ifIndex`、是否 breakout／LAG 成员、网络域与 VLAN 归属。阻塞正确的来源登记和客户归属确认；接口索引不能靠端口号推算。
- **已有：** [历史绑定与权限](modules/foundation.md)、[来源与 VLAN 重叠约束](modules/traffic.md)。
- **安全获取：** 先由机柜布线台账和管理员界面核对；获只读授权后只读取 `ifName` 子树和选定接口，按下文“有限 SNMP 样本”生成对应表。LAG／breakout 的配置位置和命令由本版本管理员本地确认，只提供相关成员摘要，不交全交换机配置或 MAC 表。
- **模拟边界：** 能检查重复绑定、跨客户和换绑后的基线；不能发现错误跳线、隐含汇聚或被错误登记成不同网络域的同一范围。

### SN-03 — Counter64、方向与刷新周期样本（实机联调前）

- **缺少／影响：** 同一个已确认接口的两组有时间戳响应，包含计数器类型、接口身份、运行时间、中断时间、状态；另需缓存／自动刷新实际配置摘要。决定数据是否可采、采样间隔是否合理。不能把缓存不变误判为零流量。
- **已有：** [标准对象与缺口语义](modules/traffic.md)、[OID 和解析代码](../backend/internal/app/traffic_collectors.go)。对象定义来自 [RFC 2863](https://www.rfc-editor.org/rfc/rfc2863.html) 和 [RFC 3418](https://www.rfc-editor.org/rfc/rfc3418.html)，不是设备支持声明。
- **安全获取：** 后续只读窗口用有限 GET 两次读取下方对象，记录实际间隔，不主动制造流量、重启或清零。若本版本支持 `show snmp auto-refresh`，由管理员本地摘取缓存是否启用及周期；3.10 手册中的默认值不能替代 3.9 实测。别名脱敏，保留 Counter64 十进制字符串和 TimeTicks 类型。
- **模拟边界：** 可覆盖回绕／复位、漏采、去重、跨周期和重映射；不能证明设备计数器持续性、硬件缓存、计入哪些帧或哪一方向代表客户上传。

### SN-04 — 计量口径与上线校核（上线前）

- **缺少／影响：** 每个账户选端口还是 VLAN 为主源、上传／下载对应方向、共享成员、额度单位／周期／时区、异常区间人工处理办法。阻塞把观察流量用于客户额度及对外解释；并不阻塞采样展示。
- **已有：** [计费与观察分离、未知和未分配区间](modules/traffic.md)、[默认关闭的业务策略](development-plan.md)。
- **安全获取：** 管理员提供少量脱敏试点拓扑和口径确认表；在授权测试窗口将端口与路由器同一时间范围的读数比较，写清流经路径和误差来源。计数器样本不应附完整客户 IP 清单。无需现在决定收费、限速、断网或断电；这些保持关闭。
- **模拟边界：** 能证明模型内不重复入账和数值精确；不能证明业务账期选择、真实拓扑或 L2／L3 计量边界正确。与 VY-02／VY-03 共用一份校核材料即可。

## VyOS

已有版本记录为 **1.5.0**，用途是 VLAN 流量与端口口径核对，见[开发计划](development-plan.md)和[流量模块](modules/traffic.md)。VyOS 官方 [1.5 版本信息](https://docs.vyos.io/en/1.5/operation/information.html)、[CLI](https://docs.vyos.io/en/1.5/cli.html)、[SNMP](https://docs.vyos.io/en/1.5/configuration/service/snmp.html)资料可作命令依据；具体构建仍须登记。文档中的 `sha` 不代表已核实 SHA-256。

### VY-01 — 实际构建和 SNMP 安全摘要（实机联调前）

- **缺少／影响：** 1.5.0 的完整构建标识、SNMPv3 算法／context／只读 view／监听与来源 ACL，决定采集兼容性。
- **已有：** [采集配置与真实访问默认关闭](modules/traffic.md)、[凭据字段和算法选择](../backend/internal/app/traffic_collectors.go)。
- **安全获取：** 获只读权限的管理员执行操作模式 `show version`，仅保留版本、构建和架构，删除主机名、UUID、序列号；在配置模式本地查看 `show service snmp`，只摘录算法、权限、view OID、监听范围别名和 context 是否使用。此输出可能包含加密口令／哈希，不能原文上交。官方所列 `/usr/share/snmp/mibs/` 可用于本地核对选定的公开 MIB，不必复制整个设备目录。
- **模拟边界：** 可验证兼容配置和拒绝路径；不能证明该镜像内 Net-SNMP 的具体加密构建、view 和防火墙实际放行。

### VY-02 — VLAN 接口及完整覆盖关系（实机联调前）

- **缺少／影响：** 试点 VLAN 的父接口／子接口、bridge／bond 关系、稳定接口名与索引、网络域、完整服务器成员及客户归属。阻塞 VLAN 主源或观察源的安全登记。
- **已有：** [完整覆盖快照与成员历史](modules/traffic.md)、[来源创建实现](../backend/internal/app/traffic.go)。
- **安全获取：** 管理员本地查看操作模式 `show interfaces`（配置模式用 `run show interfaces`），再结合相关接口的配置位置和布线表形成最小拓扑摘要。仅提供试点 VLAN／接口别名及对应成员；不要交全路由表、完整配置或客户地址。`ifIndex` 由获准的只读 IF-MIB 发现核对，不能从 `ethX.Y` 的 Y 推断。
- **模拟边界：** 可拒绝已声明范围内的跨客户与重复主源；不能发现未登记的 trunk 成员、绕行流量或不同接口重复看见的同一数据包。

### VY-03 — VLAN 计数语义与对账样本（实机联调前；正式口径上线前确认）

- **缺少／影响：** 选定 VLAN 的两组有限 Counter64 样本、方向、采样时间、设备重启／接口重建时索引与中断计数变化的已有记录。阻塞真实质量判断和与 SN2410 的对账。
- **已有：** [基线、缺口和跨周期处理](modules/traffic.md)、[采集对象](../backend/internal/app/traffic_collectors.go)。
- **安全获取：** 按下方模板读取已授权接口，保留类型和计数；管理员说明流量是否经过该 L3／VLAN 接口，以及与 SN-03 的观察边界是否相同。已有重启记录可脱敏摘要；没有就记“未观察”，本轮不要通过重启路由器、跑带宽测试或清计数获取证据。
- **模拟边界：** 可测试回退／重建、方向映射和大整数；不能验证实际内核／驱动计数语义、硬件卸载遗漏或跨设备读数差异。上线口径复用 SN-04。

### 有限 SNMP 样本：仅用于已授权的 SN-03／VY-03

先在管理员本地为选定目标配置受保护的 SNMPv3 客户端凭据与明确算法，确认客户端支持该算法；不要在参数中放认证／隐私口令。Net-SNMP 的[客户端配置手册](https://www.net-snmp.org/docs/man/snmp.conf.html)说明用户配置与口令字段，但该在线旧手册不能证明当前二进制支持哪些 SHA-2／AES 扩展。不能依赖客户端默认 MD5／DES，也不上传配置文件。`<TARGET>`、`<INDEX>` 为占位符，命令未经执行：

```sh
# 已确认用户、算法、context 来自本地受保护配置；只对批准目标执行。
snmpget -v3 -l authPriv -r 0 -t 3 -On <TARGET> \
  .1.3.6.1.2.1.1.3.0 \
  .1.3.6.1.2.1.31.1.1.1.1.<INDEX> \
  .1.3.6.1.2.1.31.1.1.1.6.<INDEX> \
  .1.3.6.1.2.1.31.1.1.1.10.<INDEX> \
  .1.3.6.1.2.1.31.1.1.1.19.<INDEX> \
  .1.3.6.1.2.1.31.1.1.1.18.<INDEX> \
  .1.3.6.1.2.1.2.2.1.8.<INDEX>
```

上述对象依次为 sysUpTime、ifName、ifHCInOctets、ifHCOutOctets、ifCounterDiscontinuityTime、ifAlias、ifOperStatus；属于标准 IF-MIB／SNMPv2-MIB，不是猜测的厂商 OID。发现接口时只允许读取已批准的 `ifName` 子树，不做整棵 MIB 扫描。输出含接口描述／客户备注时需先脱敏。

**以下是收集模板，不是真实样本；`null` 表示尚未取得，不是零：**

```json
{
  "dependency_id": "SN-03_or_VY-03",
  "evidence_kind": "template_only",
  "device_alias": "SWITCH_OR_ROUTER_ALIAS",
  "firmware_build": null,
  "observed_at_utc": null,
  "interface_alias": "INTERFACE_ALIAS",
  "if_index": null,
  "if_name_matches_enrollment": null,
  "sys_uptime": {"asn1_type": "TimeTicks", "value": null},
  "hc_in_octets": {"asn1_type": "Counter64", "decimal_string": null},
  "hc_out_octets": {"asn1_type": "Counter64", "decimal_string": null},
  "counter_discontinuity_ticks": null,
  "if_oper_status_integer": null,
  "counter_refresh_seconds": null,
  "customer_upload_maps_to": null,
  "read_error_code_without_credentials": null
}
```

## APC AP7921B

[APC 驱动](../backend/internal/drivers/apc.go)已要求固件对应 profile，没有内置猜测的 AP7921B／AP8xxx OID。Schneider 的[PowerNet MIB 说明与下载入口](https://www.se.com/us/en/faqs/FA156048/)及[Switched Rack PDU 脚本说明](https://www.se.com/us/en/faqs/FA156163/)提供厂商依据；系列示例和当前下载文件不能自动证明目标固件支持。

### AP-01 — 型号、固件和对应 MIB（实机联调前）

- **缺少／影响：** AP7921B 当前硬件／管理模块信息、AOS／应用固件版本、适用 PowerNet MIB 文件及来源／版本。阻塞生成可批准的真实 APC profile。
- **已有：** [设备基线](development-plan.md)、[驱动能力和 profile](modules/drivers.md)。
- **安全获取：** 管理员从设备本地管理界面的版本／About 页摘录型号和版本，按该版本官方支持页取得公开 MIB；给下载 URL、文件名、版本及本地 SHA-256。具体 UI 菜单和 CLI 随固件变化，本清单未验证 AP7921B 当前菜单或版本命令，不给通用写命令。序列号、设备名、IP、账号删除。
- **模拟边界：** 可测试 profile 校验与解析；不能证明 MIB 对应实际管理卡、对象可访问或枚举适用。

### AP-02 — 选定插座的精确 OID 与枚举（实机联调前）

- **缺少／影响：** 选定插座索引、只读状态 OID、控制 OID、状态整数与 on/off 的映射、获准动作整数及相关延时语义；`discovery_oid` 必须使用同一确认 profile 的只读状态对象。阻塞供电状态解释和真实控制配置。
- **已有：** [APCOutlet 结构与动作校验](../backend/internal/drivers/apc.go)、[SNMP 传输边界](../backend/internal/drivers/snmp.go)。
- **安全获取：** 对 AP-01 的公开 MIB 在管理员本地检索对象定义、INDEX、SYNTAX、MAX-ACCESS 和 DESCRIPTION，提交选定对象的短摘录及文件哈希；后续只读授权仅 GET 该状态对象，保留返回类型和整数。不执行 SET，不从其它型号或测试里的合成 OID 复制数值。控制对象及 delayed／immediate／cycle 语义需厂商文档明确。
- **模拟边界：** 可验证枚举缺失拒绝、单次 SET 尝试和超时不重发；不能证明继电器动作、物理延时和指令实际完成。平台不因驱动内部有 `hard_power_off` 就自动开放客户硬断电。

### AP-03 — 插座、供电路径与来电自启（实机联调前；动作效果上线前）

- **缺少／影响：** 每个试点插座实际连接的服务器／电源，是否共用插排、双电源、ATS 上游路径，铭凡设备当前 AC 恢复设置。阻塞安全绑定；插座 ON 不能当服务器已启动。
- **已有：** [既有 APC／ATS 需求](development-plan.md)、[供电与服务器电源分离](modules/drivers.md)。
- **安全获取：** 用机柜标签、布线台账和已有 BIOS 设置记录逐项核对，提供别名拓扑和“已确认／未确认”；不要为取证拔电或改变 ATS。真实掉电／送电验证另需准确窗口和可中断的测试服务器。
- **模拟边界：** 可保持供电状态与启动状态分离、拒绝错误归属；不能验证负载接线、UPS／ATS 行为、来电自动启动或客户 OS 恢复。

### AP-04 — 受限账号与批准配置（实机联调前）

- **缺少／影响：** 已选择的 SNMPv3 AuthPriv 算法、只读／控制权限范围、管理网 ACL、profile 引用、允许插座／服务器／动作／有效期。决定是否允许任何真实请求。
- **已有：** [外部批准 profile 校验](../backend/internal/app/operations.go)、[默认关闭和秘密保管](operations.md)。
- **安全获取：** 管理员本地审阅账号／SNMP 页面，仅提供算法与权限摘要；凭据留在服务器秘密配置。先准备 `approved=false` 的非秘密登记草稿，获得明确授权后再由管理员落地实际批准文件。不要把 SNMP community 或密码放进工单／Git。
- **模拟边界：** 可验证过期／越界请求拒绝和设备串行；不能验证实际账号最小权限、网络连通或控制窗口是否具备安全现场条件。

**AP-02 脱敏材料模板（非真实 MIB、非可运行 profile；不填任何猜测 OID）：**

```json
{
  "dependency_id": "AP-02",
  "evidence_kind": "template_only",
  "model": "AP7921B",
  "firmware": null,
  "mib_filename_and_sha256": null,
  "outlet_alias": "OUTLET_ALIAS",
  "physical_outlet_number": null,
  "state_oid_from_mib": null,
  "control_oid_from_mib": null,
  "index_definition_excerpt": null,
  "state_integer_mapping": [],
  "action_integer_mapping": [],
  "read_only_observation": null,
  "confirmed": false
}
```

## 定制 16 口 PiKVM

已读的手册、`python3-kvmd 3.142` 源码、Nginx 摘录和证据 P1～P6 均归档在[PiKVM 接入细节](pikvm-integration.md)。以下**不再索取已知静态路径**：节点状态／机箱状态、六类端口 POST、`/api/ws`、`/streamer`，以及已记录的 HID／MSD／log／redfish 路由线索。端口参数 `num=1..16`、HTTP 200 内层错误、30 秒冷却及失败端口回退风险已有代码处理。

真正缺口是当前定制副本的有效认证、具体媒体／事件协议与撤销语义。[上游 PiKVM API 文档](https://docs.pikvm.org/api/)只能用于查找思路，不能用上游事件名和状态结构替代该定制版本证据。现在可提供**已有源码／配置的本地脱敏副本**；若没有，记录缺项，不能为补资料擅自 SSH。当前真实控制台入口仍由[服务端](../backend/internal/app/console.go)拒绝开放。

### KV-01 — 有效认证及剩余 include／extras 摘要（现在必须提供：真实网关鉴权适配）

- **缺少／影响：** 已知路由实际继承的认证要求、代理头重写、Cookie／Origin 处理，以及尚未完整读到的 include／extras 是否增加旁路。阻塞确定请求白名单及凭据隔离。
- **已有：** [P5 鉴权顺序、P6 主路由与 include 关系](pikvm-integration.md)、[设备 HTTP 安全传输](../backend/internal/drivers/http.go)。不是重新收集完整 Nginx 配置。
- **安全获取：** 管理员在本地对照现有 `/etc/kvmd/nginx/kvmd.ctx-server.conf`、其 `loc-proxy.conf`／`loc-websocket.conf`／`loc-bigpost.conf`／`loc-nobuffering.conf` 和 `/usr/share/kvmd/extras/*/nginx.ctx-*.conf`，只摘取相关 location／rewrite／proxy_pass 结构与认证设置是否开启。配置文件以主文件实际 include 为准，不假定这些文件都在同一目录。保留查询正则及 `$uri` 等语法，替换地址／账号，删除秘密。用部署记录说明当前启动参数和配置来源；无需交完整 `nginx -T` 或环境转储。
- **模拟边界：** 能验证白名单、头过滤、跨会话拒绝；不能证明运行进程已加载该配置或其它网络入口不可达。生效性和旁路检查归 KV-06。

### KV-02 — 视频传输、鉴权和连接终止协议（现在必须提供：真实视频适配）

- **缺少／影响：** `/streamer` 下实际资源的方法／查询参数／响应类型；若启用 Janus／WebRTC，需具体握手、鉴权、会话销毁及可访问端点结构。阻塞可撤销视频代理；仅知道监听端口不够。
- **已有：** [ustreamer 3.16、Janus 1.0.1 与 socket 线索](pikvm-integration.md)、[平台连接登记和释放逻辑](../backend/internal/app/console.go)。
- **安全获取：** 从与安装版本一致的本地 ustreamer／Janus 配置、相关 Web 前端调用代码和 kvmd handler 摘出资源、消息结构、关闭调用及错误语义；附包版本、文件相对路径／行段、文件哈希。不要上传视频、SDP 原文中的 IP、ICE 凭据或会话 Token。未启用的通道只需明确禁用及依据，无需为验收启用它。
- **模拟边界：** 可构造流中断、长连接撤销和代次检查；不能证明关闭 HTTP／WS 就已终止 WebRTC 对等媒体，或旧客户端不再看到切换后的画面。

### KV-03 — WebSocket／HID 事件与按键释放语义（现在必须提供：真实输入适配）

- **缺少／影响：** 已知 `/api/ws` 中实际事件名、方向、载荷类型／界限、键盘鼠标状态及 release-all／断线处理语义；已有 `/api/hid/print` 的具体参数与权限。阻塞安全输入白名单及交接前清键。
- **已有：** [WebSocket 路由已知但事件未核实](pikvm-integration.md)、[模拟输入和释放](../backend/internal/app/console.go)。
- **安全获取：** 从 `/usr/lib/python3/dist-packages/kvmd/` 的当前 handler 注册、HID 插件实现和对应前端发送代码，摘取事件定义、验证器、断连／清理函数；路径由实际 import／注册追踪确认，不假设上游同名文件。只交空载荷结构或人工脱敏样例，删除输入文本、按键内容、Cookie 和认证头。不要向真实服务器发送按键来“探测协议”。
- **模拟边界：** 能检查非法事件拒绝和清理顺序；不能验证 USB HID 粘键、相对／绝对鼠标模式或物理目标是否收到释放信号。

### KV-04 — MSD 介质协议、忙碌状态与解绑完成条件（现在必须提供：真实介质适配）

- **缺少／影响：** 已知 MSD 路由的允许方法／参数／响应、上传与挂载状态、busy 判定、断开／删除规则、容量与镜像限制、清理完成可观察条件。阻塞私有 ISO 与设备介质桥接及可靠释放。
- **已有：** [已知 `/api/msd/read`、`write_remote`、`write` 路由及手册限制](pikvm-integration.md)、[平台私有介质与保护](../backend/internal/app/media.go)。现有手册容量描述不能当当前固件可用容量实测。
- **安全获取：** 从安装副本的 MSD handler／存储插件、对应配置和前端调用点提供字段与状态机短摘录；保留路径字段的语义，镜像名用别名。若已有脱敏状态响应，可包含“忙碌／已连接／断开”结构并标明来源；没有就记未观察。不要为收集样本上传、挂载、卸载镜像，也不交远程下载 URL 中的令牌或镜像字节。
- **模拟边界：** 可验证保护期间拒绝卸载、所有权和清理失败锁定；不能证明 USB mass-storage 真正断开、后台写入完成或主机缓存安全落盘。

### KV-05 — 端口物理映射及只读状态样本（实机联调前）

- **缺少／影响：** 1～16 物理编号与服务器别名的核对表、原始 `portstatus` 行序与编号关系、已知三个只读状态 API 的脱敏正常／错误结构。阻塞 `mapping_verified` 和可信目标判断。
- **已有：** [状态解析缺陷与回退端口 1 风险](pikvm-integration.md)、[PiKVM 解析与选中目标校验](../backend/internal/drivers/pikvm.go)。路径和方法已经确认，无需再次索取。
- **安全获取：** 先用台账／线缆标签与现有源码资料核对。后续经明确只读授权，由管理员对既有 `/api/node_status`、`/api/chassis_type`、`/api/chassis_status` 保存最小响应；如需 `portstatus` 原始行，先由厂商／管理员确认当前可执行文件及其只读语义。不能靠 JSON 恰好有 16 个键推断物理映射；不主动切换端口或制造错误。删除客户标识，保留编号、空行／缺项、类型和错误层级。
- **模拟边界：** 可拒绝错误、缺项和端口漂移；不能证明 HDMI／USB／电源线一致，也不能证明常量 `ON` 或回退值是真实状态。

### KV-06 — 所有旧通道撤销及带外操作边界（上线前）

- **缺少／影响：** 旧视频、WS、HID、介质、Janus／extras 连接全部失效的现场证据；管理网络旁路与前面板／其它管理员切换约束。阻塞跨客户开放共享控制台。
- **已有：** [整机独占、保护与清理要求](pikvm-integration.md)、[持久释放与核验](../backend/internal/app/console_reconcile.go)、[目标漂移观察器](../backend/internal/drivers/pikvm_console_guard.go)。目标轮询只能检测漂移，不能消除带外切换到检测之间的泄漏窗口。
- **安全获取：** 在单独批准的隔离测试窗口，用两套虚构客户／试点服务器记录旧连接撤销前后结果、各传输关闭证据和失败后的设备锁定。由管理员本地审核防火墙／反代／管理访问策略，只交路径与来源白名单摘要，不交全配置或秘密。记录装机保护跨断线、应用／网关重启后的保留；不得在生产装机中制造中断。
- **模拟边界：** 已能验证代次、持久保护、旧流登记和释放顺序；不能证明真实视频无残帧泄漏、HID 已清空、MSD 已解绑或设备外部入口没有绕过。未验证通道保持关闭，不能用超时清登记代替清理。

### KV-07 — 电源动作电气行为和维护窗口（实机联调前；真实效果上线前）

- **缺少／影响：** `close_node` 是否为短按／长按、`reset_node` 的电气含义、enable／disable 的影响与冷却、试点主板 ACPI 行为及允许动作窗口。阻塞给真实按钮赋予准确语义。
- **已有：** [源码调用关系与 30 秒约束](pikvm-integration.md)、[电源动作及结果语义](modules/drivers.md)。
- **安全获取：** 优先由厂商手册或主板控制模块说明补齐；实际动作只在已批准的测试节点／端口／窗口执行，并分别记录“已接受／观察到状态／OS 是否完成”。此清单不执行厂商命令，也不以失败重试取证。
- **模拟边界：** 可验证内层错误、超时 unknown、冷却和不重发；不能证明按键电气行为、OS 优雅关机或复位完成。仅复位后读到 ON 不关闭此项。

**KV-01～KV-04 协议摘录模板（不是固件响应，未定义任何事件名）：**

```json
{
  "dependency_id": "KV-02_or_KV-03_or_KV-04",
  "evidence_kind": "template_only",
  "installed_package_version": "VERIFY_CURRENT_CUSTOM_BUILD",
  "source_file_and_lines": null,
  "source_sha256": null,
  "known_route_prefix": null,
  "method_or_ws_direction": null,
  "event_name_from_source": null,
  "field_names_types_and_bounds": [],
  "authentication_required": null,
  "success_and_error_shapes": [],
  "close_or_detach_operation_from_source": null,
  "cleanup_complete_observation": null,
  "unverified_behavior": []
}
```

## IPMI／iDRAC

当前已实现[IPMI 适配](modules/ipmi.md)、[Redfish 适配](../backend/internal/drivers/redfish.go)及平台批准检查，不需要重新实现一套 BMC 框架。TYAN 具体主板／BMC 型号、Dell R750 当前 iDRAC 固件仍未登记。官方 [ipmitool 1.8.19 手册](https://github.com/ipmitool/ipmitool/blob/IPMITOOL_1_8_19/doc/ipmitool.1.in)、[DMTF Redfish 规范](https://redfish.dmtf.org/schemas/DSP0266_1.17.0.html)、[Dell iDRAC9 固件版本索引](https://www.dell.com/support/kbdoc/en-us/000178115/idrac9-versions-and-release-notes)是核对入口，不能据此推定现机已经支持某个动作或许可。

### BMC-01 — 型号、固件及选用接口（实机联调前）

- **缺少／影响：** TYAN 主板／BMC 型号与固件、R750 iDRAC 固件、管理账号角色、IPMI over LAN／Redfish 是否已启用，以及试点选择哪一种电源驱动。阻塞能力登记与正确手册选择。
- **已有：** [设备基线](development-plan.md)、[IPMI 能力与平台限制](modules/ipmi.md)、[批准 profile](../backend/internal/app/operations.go)。
- **安全获取：** 管理员从 BMC 已有管理界面的系统／固件信息和许可摘要摘录，按精确型号查厂商手册；删除 Service Tag、序列号、MAC、IP、许可密钥。TYAN 型号未明时不套用其它厂商的 OEM／版本命令；无需开启接口或升级固件来补表。
- **模拟边界：** 可验证驱动选择、绑定和权限；不能证明 BMC 能力、许可或账号在该固件上有效。厂商原生远程控制台许可不自动扩为本期 PiKVM 网关接入要求。

### BMC-02 — Redfish 的准确系统资源与 Reset 能力（实机联调前）

- **缺少／影响：** 选定 `/redfish/v1/Systems/<id>`、一致的 `@odata.id`、`PowerState`、Reset target、允许的 `ResetType` 或 ActionInfo 定义。阻塞绑定准确系统和安全动作映射。
- **已有：** [只选择明确 SystemPath 的实现](../backend/internal/drivers/redfish.go)、[结果语义](modules/drivers.md)。
- **安全获取：** 获 HTTPS 只读授权后，管理员使用可信 CA 访问 Systems 集合，再只 GET 选定成员与其同系统 ActionInfo；保留 JSON 字段／枚举和路径层级，系统 ID 可一致替换成别名。不要 POST Reset，不用 `-k`，不上传 Authorization／Token。只摘录上述字段，删除 UUID、SerialNumber、AssetTag、主机名和其它客户资源。
- **模拟边界：** 能测试 AllowableValues／ActionInfo、错误和目标拒绝；不能证明动作能执行、ACPI 生效或异步任务完成。PowerState On 不代表重启成功。

### BMC-03 — ipmitool 二进制来源与批准哈希（实机联调前）

- **缺少／影响：** 部署宿主／容器上实际 ipmitool **1.8.19** 的来源、补丁说明、规范绝对路径、SHA-256、目录与动态库权限。阻塞启用 IPMI 适配；安装客户端不等于批准。
- **已有：** [固定版本、重试源码审查与二进制检查](modules/ipmi.md)、[子进程实现](../backend/internal/drivers/ipmi.go)。
- **安全获取：** 仅在部署主机本地执行批准候选文件的 `-V`，用 `sha256sum /absolute/path/to/ipmitool` 或 PowerShell `Get-FileHash -Algorithm SHA256 -LiteralPath <绝对路径>` 记录哈希；附包来源／发行版补丁及属主／写权限摘要。此步骤无需接触 BMC，也不传密码。不要仅凭版本字符串批准另一个二进制。
- **模拟边界：** 已能测试哈希、路径、参数和子进程环境；不能替代对真实发行版补丁、动态库、目录可写性和部署供应来源的确认。

### BMC-04 — LANplus cipher 17、权限和只读响应（实机联调前）

- **缺少／影响：** 目标是否支持 cipher 17、批准角色 OPERATOR／ADMINISTRATOR、UDP 端口／网络 ACL、一次状态读取的脱敏结果。阻塞实际 IPMI 会话；不自动切 cipher 或提升权限。
- **已有：** [固定 LANplus 参数和不盲目重试](modules/ipmi.md)、[实际命令构造](../backend/internal/drivers/ipmi.go)。
- **安全获取：** 先由 BMC 文档／界面确认；后续已授权窗口可用下列只读示例，密码交互输入，不用 `-P`、高等级 `-v` 或原始包转储。可由管理员按真实 channel 使用只读 `channel getciphers ipmi <CHANNEL>` 核对支持列表；此查询本身不能替代指定 cipher 的成功认证。缺少 cipher 17 时记录不支持，不以旧 cipher 重试。
- **模拟边界：** 能验证固定参数和超时 unknown；不能证明 BMC 固件遵守单次指令语义或实际链路丢包表现。只读命令成功不授权任何电源写操作。

```sh
# 模板，尚未执行；角色、端口、路径与目标必须来自批准记录。
/absolute/path/to/ipmitool -I lanplus -C 17 -R 1 -N 2 \
  -H <APPROVED_IP> -p <UDP_PORT> -U <LOCAL_ACCOUNT> -L OPERATOR \
  -a chassis power status
```

### BMC-05 — OS 关机、复位完成与未知任务核验（上线前）

- **缺少／影响：** 试点服务器 ACPI／OS 关机行为、独立启动／复位完成观察方式、超时后的人工核验责任及禁止重发流程。阻塞向客户宣称真实动作完成。
- **已有：** [IPMI 动作边界](modules/ipmi.md)、[持久任务恢复](operations.md)、[Redfish 结果处理](../backend/internal/drivers/redfish.go)。
- **安全获取：** 先提供 OS／主板设置的脱敏摘要与维护流程，真实动作在可中断测试机和明确窗口验证，记录动作时间、BMC 受理、独立观察结果。不要交完整系统日志／业务内容，也不要求为证明超时而对生产服务器断网。
- **模拟边界：** 已覆盖 accepted／completed／target_reached 分离与 unknown 不补发；不能证明 OS 实际关机或重启。保留未知是合格失败处理，不得以重复命令“确保执行”。

**BMC-02 最小响应摘录模板（非真实响应；`null` 不代表固件值）：**

```json
{
  "evidence_kind": "template_only",
  "system": {
    "@odata.id": "/redfish/v1/Systems/SYSTEM_ALIAS",
    "PowerState": null,
    "Actions": {
      "#ComputerSystem.Reset": {
        "target": null,
        "ResetType@Redfish.AllowableValues": [],
        "@Redfish.ActionInfo": null
      }
    }
  },
  "action_info_if_used": null,
  "http_status": null,
  "credential_headers": "OMITTED"
}
```

## WHMCS

[模块与安装说明](../integrations/whmcs/README.md)、[实际离线验证](modules/whmcs.md)已有身份／归属校验、HMAC、生命周期、流量区间图、电源任务和门户。独立 Smarty 4.5.8／5.8.5 已实际渲染模板，不再把“没有 Smarty”当成缺口；尚缺的是**真实授权 WHMCS 宿主**。官方 [CurrentUser](https://developers.whmcs.com/advanced/authentication) 与 [Client Area Output](https://developers.whmcs.com/provisioning-modules/client-area-output)说明集成入口，不能代替安装实例验收。

### WH-01 — 实际宿主版本和主题（实机联调前）

- **缺少／影响：** WHMCS 精确版本、对应 PHP／扩展、Smarty／主题及 override 情况、运行方式 FPM／Apache、相关身份或客户端 hook。阻塞确认模块在安装实例上兼容。
- **已有：** [模块前置条件](../integrations/whmcs/README.md)、[Smarty 实际渲染证据](modules/whmcs.md)。
- **安全获取：** 管理员从管理界面的系统信息和部署清单手工摘录版本／扩展／主题名称，按该 WHMCS 版本的官方兼容矩阵核对。比如官方 [8.10 系统要求](https://docs.whmcs.com/8-10/getting-started/system-requirements/)只适用于 8.10，不能用来声明其它版本或本次本地 PHP 8.5 环境受支持。不要交 `phpinfo()`、环境转储、许可密钥、数据库连接或整套商业源码。
- **模拟边界：** 可检验 PHP／Smarty 语法与变量转义；不能验证安装主题、商业框架内部权限及本地 hook 改写。

### WH-02 — 有许可的隔离实例和客户权限场景（实机联调前）

- **缺少／影响：** 可安装模块的测试实例、两套虚构客户／服务、普通客户与授权联系人／代理登录场景、管理员测试权限和窗口。阻塞 WHMCS 身份到 BarePulse 归属的真实验证。
- **已有：** [CurrentUser 与 tblhosting 校验](../integrations/whmcs/barepulse/lib/ClientArea.php)、[离线契约](../integrations/whmcs/tests/contract.php)、[跨语言契约](../integrations/whmcs/tests/platform_contract.php)。
- **安全获取：** 管理员在已有合法测试环境创建合成账号和服务，仅映射 BarePulse 模拟资产；提供访问安排和测试角色摘要，凭据通过管理员批准的秘密渠道交付，不能贴到仓库／聊天。测试环境禁用实际收费、对客户发信和生产 provisioning，避免复制真实客户库。
- **模拟边界：** 已可验证跨客户拒绝、重放、CSRF 和参数污染；不能证明真实 WHMCS 的联系人授权、SSO／管理员代登和产品页面入口都保持同样限制。

### WH-03 — 服务端传输与秘密注入环境（实机联调前；生产配置上线前复核）

- **缺少／影响：** WHMCS 服务端到 BarePulse 的精确 HTTPS 入口、可信 CA／主机名匹配、时钟同步、FPM 环境变量传递、集成秘密引用及轮换负责人。阻塞签名调用和短时门户凭证。
- **已有：** [API 客户端传输校验](../integrations/whmcs/barepulse/lib/ApiClient.php)、[秘密引用及停用／轮换](modules/whmcs.md)、[BASE_URL 与 Cookie 配置](operations.md)。
- **安全获取：** 管理员本地审核指定 FPM pool／服务配置，只报告“选定引用变量已注入／缺失”、运行用户、CA 来源、时间同步状态和入口别名。只在批准实例用模块的合成请求验证，不导出整个环境或输出秘密值。配置若由 FPM 清理，显式传递所需单项，不关闭所有环境隔离。
- **模拟边界：** 可验证 HMAC、nonce、过期、拒绝重定向和密钥轮换撤销；不能证明真实代理／时钟／CA／FPM 配置正确。浏览器仍不持有集成秘密或设备凭据。

### WH-04 — 服务映射、生命周期和宿主验收（上线前）

- **缺少／影响：** 试点 integration／service／client／server 别名对应表、暂停／恢复／终止的业务流程、保护装机和 unknown 电源任务冲突的处理人，以及安装实例上的实际结果。阻塞向客户开放门户和自动生命周期调用。
- **已有：** [访问生命周期规则及必测场景](modules/whmcs.md)、[后端映射校验](../backend/internal/app/whmcs.go)、[门户单次兑换](../backend/internal/app/portal.go)。
- **安全获取：** 先用 WH-02 的合成账号／模拟资产逐项验证越权服务 ID、重复提交、响应丢失、暂停后旧门户、保护任务阻止终止、密钥轮换及恢复不复活旧票据；仅保存状态码／任务别名／预期和实际结果。无需现在确认超额收费策略，暂停只改访问，真实物理关机／断网不在默认流程内。
- **模拟边界：** 平台契约与保护持久化已覆盖；不能证明宿主钩子顺序、实际主题交互或真实控制台隔离。最后一项依赖 KV-06，WHMCS 页面成功不能代替设备隔离验收。

## 材料登记与关闭条件

推荐每次提供一个小包：依赖编号、设备／实例别名、精确版本、来源路径／官方 URL、取得日期、是否为已有静态资料或获准只读观察、脱敏结果、仍未知项。只提供资料时不带 `approved=true`；批准文件、秘密和原始证据由部署者单独保管。

```json
{
  "dependency_id": "KV-03",
  "evidence_kind": "template_only",
  "device_or_instance_alias": "TEST_ALIAS",
  "exact_version": null,
  "source_path_or_official_url": null,
  "collected_at_utc": null,
  "authorization_reference_if_observed": null,
  "sanitized_excerpt": null,
  "still_unverified": [],
  "contains_secrets": false
}
```

`evidence_kind` 在真实登记时使用 `static_document`、`local_config_review`、`authorized_read_only_observation` 或 `authorized_integration_test`，不把示例当成实测。条目关闭需写明哪份证据解决了哪个字段；若只取得静态材料，状态记“资料齐备／离线契约可实现”，不能写“实机通过”。未来配置／固件变化时重新检查受影响条目，不复用旧的物理端口和撤销结论。

本清单的网页核验日期均为 **2026-10-19**：NVIDIA 参考版本为 3.10.4408 LTS（目标 3.9.3202 待对应核对）；VyOS 为 1.5 文档；Schneider 为官方 FAQ 与 PowerNet 入口；PiKVM 为上游参考而非定制固件契约；IPMI 固定 1.8.19 源码／手册；Redfish 为 DMTF DSP0266 1.17.0；WHMCS API 文档及 8.10 兼容矩阵仅按所标版本使用。没有从这些参考页推导未核实的设备版本、私有 OID、事件名称或现场测试结果。
