# 可复现离线源码交付

`scripts/export-offline-package.py` 只从当前仓库**已提交的 HEAD**生成源码 ZIP、完整历史 Git bundle 和逐文件清单。需要 Python 3.10+ 与本地 Git，不依赖 PostgreSQL、Docker、第三方 Python 包或网络。它不执行应用测试、不读取真实设备、不部署、不发布，也不替代[数据库与媒体备份恢复](../backup-restore.md)。

## 导出命令

先完成必要验证和提交，确认工作区干净。输出目录必须尚不存在，父目录必须已经存在；输出须在仓库和 Git 元数据目录之外，包括解析符号链接后的实际位置。脚本不会创建父目录、覆盖旧交付、修改分支或清理用户文件。以下命令在仓库根目录运行；将示例输出替换为已有父目录下的绝对新路径，不得包含 `..` 路径组件。

```sh
python3 -B scripts/export-offline-package.py \
  --repo . \
  --output /tmp/BarePulse-offline-20261019
```

Windows PowerShell（此例要求 `C:\BarePulse-deliveries` 已存在）：

```powershell
python -B ./scripts/export-offline-package.py `
  --repo . `
  --output 'C:\BarePulse-deliveries\BarePulse offline 20261019'
```

`--repo` 默认为当前目录，也接受仓库内的子目录；参数通过独立 argv 传给 Git，带空格的路径不经 shell 拼接。`--timeout-seconds` 为每条本地 Git 命令的超时，默认 300，范围 30–3600 秒。退出码 0 只表示这次交付文件核验通过；非零表示拒绝或失败。

可用 `--expected-head <完整小写SHA>` 固定预期快照。它必须等于当前 HEAD，不接受缩写、分支名或其它历史提交，避免意外交付错误版本。需要历史版本时，先自行在适当的独立 checkout/worktree 中检出该提交，再运行导出；脚本不会代替用户 checkout。支持 detached HEAD，清单的 `snapshot.branch` 为 `null`，bundle 的 `HEAD` 仍指向准确提交。

可选 `--remote-ref refs/remotes/origin/codex/barepulse-implementation` 检查**本地已有远端跟踪引用**与 HEAD 是否同树。两者提交 ID 可以不同，但 tree 必须相同。脚本不会 fetch、push、访问远端或推断该引用仍是远端最新状态。引用不存在、tree 不同或导出期间引用变化都会失败。

## 输出与核验

| 文件 | 内容 |
| --- | --- |
| `BarePulse-source.zip` | 固定 `BarePulse/` 前缀下，已提交 HEAD 中的全部普通跟踪文件 |
| `BarePulse-checkpoint.bundle` | HEAD 及其全部可达提交历史；不含其它不相关分支、工作文件或远端配置 |
| `manifest.json` | 分支、完整 local HEAD、tree、Git 对象格式、工作区行尾配置、可选本地远端对照、每个源码文件的路径/大小/SHA256/Git blob ID/模式，以及 ZIP/bundle 的大小与 SHA256 |

导出器实际执行的核验包括：

- 导出前和结束前检查 HEAD、分支、工作区一致。未暂存、已暂存以及未跟踪且未忽略的文件都会阻止导出。不会清除 `assume-unchanged` / `skip-worktree` 来掩盖状态，这类索引直接拒绝。
- 为正确识别 Windows CRLF checkout，只读查询并校验有效 `core.autocrlf` / `core.eol`，包括常规系统、用户及仓库配置的优先级，再通过独立 `-c` 参数传入。允许 `autocrlf` 的布尔值或 `input`，`eol` 的 `lf` / `crlf` / `native`；不支持的值明确拒绝。清单 `snapshot.worktree_line_endings` 记录采用的值，其余系统/用户配置仍被禁用，不继承 hooks、filters、credential helpers 或任意 `GIT_*` 环境覆盖。
- 用 `git ls-tree` 读取提交文件清单，通过 `git archive` 生成 ZIP，然后比较完整文件集合、逐文件 Git blob 身份、SHA256 和 ZIP CRC。`export-ignore` / `export-subst` 若导致漏文件或内容变化，明确失败；不会把工作区内容混入源码包补齐。
- 行尾设置只用于保持工作区语义；生成 ZIP 时单独禁用 autocrlf 转换并指定 LF，随后核对提交 blob，避免把工作区 CRLF 写进原本为 LF 的源码归档。不会重写工作文件或执行 renormalize。
- 在全新临时 bare 仓库验证 bundle、解包、检查 HEAD/tree 并执行 `git fsck --full --strict`。这样不会因源仓库已经拥有对象而漏掉 bundle 前置依赖。临时验证库只在系统临时目录创建，完成后仅清理自身临时目录。
- 拒绝浅克隆、子模块、符号链接、非普通跟踪对象、路径穿越、Windows 特殊路径、大小写/Unicode 文件名冲突，防止交付包解压到不同平台时混淆目标。这些类型不被静默省略。

只导出 Git 对象，不扫描或复制工作目录，因此被忽略且从未提交的 `.env`、私有 ISO、数据库备份和工具链不会进入 ZIP 或 bundle。**bundle 保留可达历史**：如果秘密曾经提交到这段历史，普通 `.gitignore` 无法移除它；本工具不是秘密扫描器，也不会自动重写历史。交付前必须处理已提交秘密，不能宣称仅靠此工具完成了脱敏。

生成过程中先写 `EXPORT-INCOMPLETE` 标记，全部核验成功后才写 `manifest.json` 并移除标记。中途失败保留新建目录与 `export-failure.json`，便于诊断；这样的目录不能作为成功交付。重跑使用新目录，不覆盖失败证据。导出前拒绝的脏工作区、错误参数、已有输出目录等不会创建交付目录。

在包含 bundle 的交付目录中运行以下命令恢复源码及历史；确认 `BarePulse-restored` 尚不存在：

```sh
git clone BarePulse-checkpoint.bundle BarePulse-restored
git -C BarePulse-restored rev-parse HEAD
git -C BarePulse-restored rev-parse 'HEAD^{tree}'
```

把结果与清单的 `snapshot.local_head` / `snapshot.tree` 比较。bundle 只携带 `HEAD` 名称；需要原开发分支名时，核对清单后在新克隆中自行创建，不修改原仓库。

## 测试证据输入

导出器不会硬编码旧交付的 PASS，也不会执行证据里写出的命令。默认 `test_evidence.checks=[]`；应用检查是否运行始终记录为 `artifact_verification.application_tests_run=false`。

通过 `--evidence <JSON文件>` 显式传入已经脱敏的证据说明。文件最多 1 MiB、100 条记录，仅支持以下字段：

```json
{
  "checks": [
    {
      "name": "本地 PostgreSQL 回归",
      "reported_status": "NOT_RUN",
      "scope": "仅离线模拟与专用测试库；此例未执行",
      "evidence": "填写真实日志编号或已核对的CI结果链接"
    }
  ]
}
```

每条必须有 `name`、`reported_status`、`scope`、`evidence`，状态只接受 `PASS`、`FAIL`、`NOT_RUN`、`BLOCKED`。可选 `tested_commit` 为完整小写提交 SHA。导出器保存原输入 SHA256，并计算 `matches_exported_head`；缺少或不同提交时为 false，不能因此推断测试覆盖当前源码。字段均为非空字符串，最长 2000 字符；未知字段被拒绝。

这些记录统一标为 `verification: NOT_PERFORMED_BY_EXPORTER`，只是调用者提供的声明；链接不会被访问，结果不会被升级为导出器验证过的测试。仅归档和 bundle 的实际检查写入 `artifact_verification.status`。不要在证据字段中放口令、完整生产日志、连接凭据或私有客户资料。原证据文件不复制进包。

## 回归命令

在仓库根目录执行：

```sh
python3 -B -m unittest discover -s scripts/tests -p test_export_offline_package.py -v
```

测试只在 `TemporaryDirectory` 创建专用 Git 夹具，不删除或改写当前真实仓库。覆盖真实 ZIP/bundle 的导出与恢复、逐文件哈希、忽略秘密排除、脏工作区、已有/仓库内输出、路径穿越、detached HEAD/完整 SHA、仅本地远端 tree 核对、证据与实际验证分离、属性导致内容差异、索引隐藏标记、子模块/符号链接和损坏归档。测试机器缺少目录符号链接权限时，该单项明确标记 skip，其它守卫仍执行。

2026-10-19 初版实际执行：Windows、Python 3.12.14、Git 2.56.0.windows.1，以上命令 11 项全部通过（54.161s），没有 skip；包含两个不同提交但同 tree 的本地远端对照。首轮子模块夹具缺少工作目录，先被脏工作区守卫拒绝，导致预期错误类型断言失败；补全专用夹具后完整重跑通过，没有放宽守卫。该结果只证明交付工具，不代替应用回归或实机验收。

同日 CRLF 回归：专用仓库通过隔离用户配置 `core.autocrlf=true` / `core.eol=crlf` 真实 checkout，确认正常 Git status 干净后，初版导出器仍报 Dirty worktree，已实际复现。夹具建立干净 index 所用的 renormalize 仅在测试仓库执行；导出器本身不修改 index 或工作文件。加入行尾白名单后，定向测试又发现 archive 会继承转换而导致 blob 不匹配，因此对归档单独禁用转换。最终新增 `test_global_autocrlf_clean_crlf_checkout_exports_committed_lf` 与 `test_line_ending_config_whitelist_excludes_helpers_and_rejects_invalid_values`，验证 ZIP 保留提交 LF、工作区仍为 CRLF、任意 Git 环境覆盖无效、非白名单 helper 未继承及非法配置拒绝。完整 **13 项 PASS（60.560s），无 skip**。
