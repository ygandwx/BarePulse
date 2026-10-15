# 运行、恢复与交付

## 启动及配置

README 给出 Compose 与原生启动命令。迁移必须先执行；`schema_migrations` 记录已应用版本，迁移由 PostgreSQL advisory transaction lock 串行。禁止编辑已部署的迁移；后续变化添加新文件。

`BASE_URL` 必须精确匹配浏览器入口（含协议/端口），用于 Origin 验证与 WHMCS 门户 URL。生产反向代理终止可信 TLS 时设置 `SECURE_COOKIES=true`，仅向代理开放应用端口；不要关闭证书验证。当前开发 Compose 仅绑定宿主 127.0.0.1。后端从不转发客户端设备认证头。

`ALLOW_REAL_DEVICES=false`、`ALLOW_DEVICE_WRITES=false` 为默认值。录入真实地址不会启用网络访问。电源运行还要求外部 `BAREPULSE_DEVICE_PROFILES_FILE` 中批准的设备/服务器/端口/动作/时间窗与当前绑定一致；缺资料的 APC OID 和枚举绝不提供猜测默认值。即使代码支持某驱动，本次任务也没有授权打开这些开关。IPMI 另需[固定版本客户端及 SHA256 审批](modules/ipmi.md)，容器虽安装客户端但没有默认授权或凭据；未知的真实控制台媒体路径保持 blocked。

设备与集成秘密不放数据库：电源 profile 文件和 SNMP/WHMCS 环境变量由部署者保管，权限限制为服务账号可读，备份与数据库分开加密。`credential_ref` 只表示引用。公开仓库只含示例和测试密钥，不应提交真实内容。

## 安全任务恢复

电源任务在派发前持久化目标版本及设备占用。进程中断留下 running/verifying 时，新 worker 将其标记 unknown，保留设备阻断，不自动重发。仅未执行 queued 可取消；unknown 必须核对旧执行者确已停止和实际设备状态，再由管理员填写理由处理。这个处理只清理平台阻断，不补发电源指令。

共享控制台的 lease、protection、generation 与介质状态保存在 PostgreSQL。关闭页面不结束装机。保护作业必须显式结束，然后释放：撤销票据 → 停止旧流 → 清除键鼠 → 解绑介质 → 清除目标 → 释放设备。`gateway_connections` 有残留时不能交接；未知行不按时间自动删除。模拟故障可通过管理员“核验模拟清理”恢复，仍要求没有登记中的旧连接。实机通道没有此绕过入口。

多实例同时运行时，电源 worker 使用持久 PostgreSQL advisory leader lock，设备 lease 不因任期失效而被转授。数据库恢复可能复活旧状态：恢复期间必须保持设备访问和写操作关闭；确认所有旧应用/网关进程停止，核对任务、保护作业及网关登记后再恢复服务。未知状态不能直接改成 succeeded。

## 备份与离线恢复演练

Windows 可执行 [备份恢复脚本与验证说明](backup-restore.md)，它使用同一导出快照比较全部关键表内容，并校验媒体哈希；只恢复到不存在的新库。不能用单纯 pg_dump 成功代替恢复验收。

先停止应用写入，保留数据库运行。数据库和媒体文件必须来自同一停写时间点；单独保存密钥，勿写入命令行或备份日志。以下 PostgreSQL 命令通过 `PGHOST/PGPORT/PGUSER/PGDATABASE` 和受保护 `.pgpass` 提供连接，示例文件在 `work/`：

```sh
pg_dump --format=custom --file=work/barepulse.dump
# 另行备份 MEDIA_DIR（私有介质，不提交 Git）。
createdb barepulse_restore
pg_restore --exit-on-error --no-owner --dbname=barepulse_restore work/barepulse.dump
```

恢复到新库；禁止覆盖当前库演练。以恢复库设置 `DATABASE_URL`，两设备开关保持 false，执行 migrate，检查 users/servers/bindings/traffic_ledger/operation_tasks/console_sessions 的数量、任务状态、保护原因、介质哈希。演练证据记在 implementation-status。删除演练库只能在确认名称为本次新建库并不再需要后执行；默认保留以便审核。

## 运维观测

`/healthz` 仅检查数据库，绝不探测设备。任务中心提供 queued/running/unknown 与审计；设备列表显示占用、阻断和冷却。流量详情返回原始质量、缺口、未分配跨周期字节和采集时间。没有数据不显示为正常零流量。额度预警记录去重事件；自动收费、限速、断网和断电没有启用路径。

## 发布前外部验收

必须另行取得具体设备、目标端口、动作、服务器与时间窗授权；核对供电路径、PiKVM端口映射与认证、APC对应固件MIB、BMC能力。真实控制台必须枚举并验证所有视频/键鼠/虚拟介质路径及带外切换，不具备撤销证据的 WebRTC/Janus/extras 不开放。WHMCS 的实际版本、Smarty模板、用户身份和生命周期需在授权测试实例验证。未达这些条件不可宣称生产可用。
