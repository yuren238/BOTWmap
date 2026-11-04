# 适配 Savage Wind Recomp（原生重编译版）说明

本 fork 把 [yalincc/BOTWmap](https://github.com/yalincc/BOTWmap) 的定位/进度能力扩展到
[Revivedx/SavageWindRecomp](https://github.com/Revivedx/SavageWindRecomp)（《旷野之息》Switch 1.9.0
静态重编译原生 PC 版）。地图本体（瓦片、点位、图标、进度口径）**完全复用**，因为它是同一款游戏、
同一版本 1.9.0 的地图数据。

- 地图前端：`app/`（纯静态，未改动数据结构）
- 定位程序：`xnavi/`（Go，新增 `platform_savagewind.go`）
- 存档：`<游戏目录>/save/save_0/<0..5>/game_data.sav`

---

## 1. 为什么能复用，以及哪里不一样

Savage Wind 不是模拟器，而是静态重编译：Switch 的机器码在本地被翻译成 x86-64 原生代码，
但**内存布局按 guest 地址原地映射**。启动日志可以直接印证：

```
[gamelib]   main     7000000000-7001e98000 125144 functions
[gamelib]   subsdk0  7001e98000-7002504000 13936 functions
[fs] mounted host directory save/save_0
```

实测该进程内有一块 **0x723FF00000 起、约 3121MB 的已提交 RW 私有区域**，即游戏 3GB guest RAM；
读写小端，和 Switch 一致。因此 xnavi 原有"扫 guest 内存找坐标三元组"的思路可以直接用，
**不需要模拟器的地址翻译层**。

真正需要重写的只有三件事：进程/内存枚举方式、存档根路径、定位门槛（因为内存结构指纹不同）。

## 2. 数据源与格式（实测）

| 项目 | Savage Wind 实际情况 | 适配动作 |
| --- | --- | --- |
| 存档位置 | `<游戏目录>/save/save_0/0..5/game_data.sav`（`save.lock` 锁文件） | 新增 `SaveRoots()`：`<exe 目录>/save` |
| 存档格式 | 与 Switch 版一致：版本字段 `0x4730`（1.9.0）、小端、`0x0C` 起 8 字节 `[hash][value]` 条目、`PLAYER_POSITION`(0xA40BA103) 连续三块 | 版本白名单补 `0x4730`（其余逻辑不变） |
| 主档策略 | 6 个槽轮转写，槽号不可靠 | 复用 Cemu/Eden 的"最新 mtime + 排除空档"策略 |
| 内存块 | guest 地址 `0x7000000000+`，RW 私有，3121MB | 新增 `Blocks()`：guest 区间 + RW + 大小阈值 |
| 字节序 | 小端 | `LittleEndian() = true` |
| 已知固定偏移 | 无社区样本（社区偏移只对 Cemu/Ryujinx 有效） | `KnownOffsets` 返回空，走扫描；锁定后由 `known_botw_savagewind.json` 记住相对偏移 |
| 定位门槛 | 林克 Transform 仍是 **0x40 步长「位置 + 单位四元数」环缓冲**，实测连续 33 条（`Run=33`），与 Eden/Ryujinx 同构 | 直接沿用 `Run>=2` 门禁（能挡掉 guest RAM 里大量无四元数的"物件摆放记录"） |

存档解析实测（6 个槽都通过 `gameValidTriple`）：

```
0 (-629.7, 171.4, 1853.4)   3 (-629.0, 171.5, 1849.2)
1 (-565.9, 175.0, 1840.6)   4 (-623.0, 176.3, 1923.1)
2 (-659.1, 170.2, 1823.7)   5 (-776.0, 174.8, 1560.0)
```

进度同步实测（同一存档）：

```
[progress] reference table: 1078 points (shrine 136, tower 15, korok 900, memory 23, beast 4)
[progress] save=...\Savage Wind\save\save_0\0\game_data.sav
[progress] done: shrines 4/136, towers 1/15, koroks 1/900, memories 0/23, beasts 0/4
```

## 3. 地图数据加载方式

地图前端不需要读游戏目录：瓦片/图标/点位全部在 `app/assets`、`app/data` 里，`index.html` 直接打开即可。
与游戏目录的关系只有两条：

1. **实时定位**：xnavi 读进程内存 → `http://127.0.0.1:8766/pos` → 地图页 `app/js/live.js` 轮询显示红点。
2. **收集进度**：xnavi 解析 `<游戏目录>/save/.../game_data.sav` → `/progress` → 地图页勾选已收集点位；
   网页端也支持手动上传同一个 `game_data.sav`（`app/js/save_upload.js`，已含 0x4730 版本）。

xnavi 启动时会把 `app/` 挂成本地静态站点（日志 `[asset] BOTWmap assets mounted`），
所以断网也能用；局域网地址 `http://<内网IP>:8766/botw/?follow=1` 可直接给手机/平板用。

## 4. 代码改动清单

| 文件 | 改动 | 说明 |
| --- | --- | --- |
| `xnavi/platform_savagewind.go` | 新增 | 平台驱动：进程 `savagewind`、guest RW 块枚举、存档根、小端 |
| `xnavi/main.go` | 修改 | `--emu=savagewind`、`--savagewind-save-dir=`、auto 探测把 Savage Wind 排在首位 |
| `xnavi/game_botw.go` | 修改 | `KnownOffsets` 该平台返回空；`SaveAnchors` 走"最新 mtime"分支 |
| `xnavi/watch_ryujinx.go` | 修改 | 管道泛化为 `Platform` + `label` + `minRun`，新增 `NewSavageWindTracker`（Run 门槛 0），日志按平台名输出 |
| `xnavi/watch.go` | 修改 | 平台分派新增 `SavageWind`；等待提示补平台名 |
| `xnavi/savefile.go` | 修改 | 版本白名单补 `0x4730` |
| `xnavi/ADAPT-SAVAGEWIND.md` | 新增 | 本文档 |

前端零改动：地图数据、点位、进度表、上传解析都不依赖平台。

## 5. 使用

```bat
REM 1) 地图：直接打开
app\index.html

REM 2) 实时定位（游戏已进入存档后运行）
cd xnavi
xnavi.exe --emu=savagewind
REM    存档路径非默认时： --savagewind-save-dir="D:\Savage Wind Recomp\Savage Wind\save"
```

首次定位需要一次"走动确认"：程序会先锁一个候选地址，日志提示
`【请在游戏内移动角色以完成验证】`，走动 3~5 步后出现
`[lock] ... 移动确认成功 ... 【已完全锁定】`。之后每次启动会优先用
`known_botw_savagewind.json` 里记下的相对偏移秒锁。

## 6. 验证状态

| 项 | 状态 | 证据 |
| --- | --- | --- |
| 进程识别 / 内存块枚举 | 已通过 | `[platform] selected: SavageWind`；guest 地址 `0x723FF00000` 起 3121MB RW 区域 |
| 存档发现 + 解析（1.9.0 / `0x4730`） | 已通过 | 6 个槽坐标全部通过 `gameValidTriple`，`saveSource=自省命中: Savage Wind 目录` |
| 进度表重建 | 已通过 | `/progress`：shrine 4/136、tower 1/15、korok 1/900，与存档一致 |
| 地图页本地加载 | 已通过 | `/botw/` 与 `/botw/js/live.js` 均 200；`[asset] BOTWmap assets mounted` |
| 实时坐标锁定 | 已通过 | 锚点 120m 窗口 3.4~4.4s 锁定；`/pos` 返回 `gx=-647.3, gy=174.1, gz=1876.1, copies=5, source=player` |
| 真身判定（走动） | 已通过 | 锁定地址 `0x72E920DA70` 与另一副本在玩家走动后同步位移 **71.6m / 71.9m**，方向一致，确认锁到林克本体而非静态数据 |
| 记录结构指纹 | 已通过 | 锁定点处为 0x40 步长「位置 + 单位四元数」环缓冲，连续 33 条（Run=33），静止时坐标仅抖动 ±0.01m |

## 7. 已知限制与待办

1. **锁定后需要走动才算 `verified`**：这是上游设计（Ryujinx/Eden 同款）。首帧锁定已能给出正确位置
   （`source=player`），但 `verified` 要玩家走动 3~5 步后才置真；站着不动时红点位置正确、状态仍为未确认。
2. **锚点窗口有限**：默认 ±120m；存档超过 5 分钟直接按 400m 起步，窗口内候选全静止时自动放大到
   400m / 3000m。存档越新越接近当前位置，锁定越快；建议进游戏先手动存一次档（日志会提示"存档已 N 分钟前写入"）。
3. **首次扫描成本**：3.1GB 全量窗口扫描约 3.4~4.4s（8MB 分块 / 4 worker）；锁定后只读 1 个地址，
   开销可忽略。`known_botw_savagewind.json` 会让二次启动秒锁，但前提是 guest 块基址变化时偏移稳定
   （实测两次启动偏移一致）。
4. **只在 Windows + Savage Wind 0.4.1-alpha.4 / 游戏 1.9.0 实测**：Savage Wind 仍是 alpha，
   若后续版本改变 guest 映射方式，需重新核对 `Blocks()` 的筛选条件（guest 区间 / RW / 大小）。
5. **xnavi-gui（Wails 壳）未同步**：GUI 的平台下拉、存档路径输入还没有 Savage Wind 项，
   目前用命令行 `--emu=savagewind` / `--savagewind-save-dir=` 等价可用。
6. **存档槽选择**：按 mtime 降序取第一个 `Playtime>0` 的档作为主锚（正在玩的档一定最近写入），
   有神庙进度的旧档只作备用锚；`Playtime==0` 的真空档才跳过。大师模式新档 `shrines=0` 但
   `Playtime>0`，属于正常档，不能当空档排除。如果玩家改用别的槽，
   `known` 与进度会在 2s 内自动跟随。若需要手动指定，可用 `--savagewind-save-dir=` 指向具体 `save_0` 目录。

## 8. 两个「坐标冻结」故障的实际根因（2026-11-04 实测）

Savage Wind 会在 guest RAM 里留下多份与真身同构的历史副本（0x40 步长、位置 + 单位四元数、Run 甚至更长），
它们和真身唯一的区别是**字节永不变化**。两次「红点钉死不动、永远 verified=false」都是锁到了这类副本：

| 现象 | 根因 | 处理 |
| --- | --- | --- |
| 锁到读档点，走动不跟随 | 「上次存档位置」快照结构与真身一致，且紧贴锚点，旧排序（Run→Quat→距离）让它排第一 | 锁前做活跃度采样（1KB 窗口、间隔 150ms），活对象优先 |
| 186 组候选**全部** 0 字节变化 | 存档 79 分钟未更新，真身已走出锚点 157m，120m 窗口里只剩上次会话的静态残留 | 窗口内全静止 → 不锁定，自动放大到 400m/3000m；存档 >5 分钟直接 400m 起步 |
| 锁定后位置正确但 45 秒不动 | 锁到瞬间还在写、随后停止写入的记录 | 未确认 + 记录静止 + **其它候选仍在刷新** → 掉锁重扫；若其它候选也静止（菜单/背包/加载，世界暂停）则保锁，避免红点凭空消失 |

定位真身的判据（实测）：真身坐标**每 100ms 都在变**（持续 30 次采样无重复值），静态副本恒定不变。

## 9. 大师模式新档「永远全静止」的两个叠加根因（2026-11-04 23:45 修复）

玩家新开大师模式档，日志稳定刷「候选共 6049 组，前 64 组 0.15s 内均无字节变化」→ 永不锁定。
实测是两处独立缺陷叠加：

| 层次 | 根因 | 证据 | 处理 |
| --- | --- | --- | --- |
| ① 锚点选错 | 选档规则让「shrines>0」优先于「mtime 最新」，刚开的大师模式档 shrines=0，永远输给一小时前的普通档 | 槽 7（playtime 879s，mtime 23:39）是正在玩的档，锚点却取槽 4（67 分钟前）：`(-1050,249,1821)` vs `(-783,189,1803)`，偏离 274m | 主锚改为 **mtime 最新且 Playtime>0**；有进度的旧档降为备用锚 |
| ② 采样名额被占满 | 候选排序是 Run 降序，静态快照 Run=33，真身 Run 只有 2~6；活跃度采样只取前 64 名，64 个名额清一色静态 | 全内存探针：锚点 60m 内 4802 个候选有 **73 组在跳动**，而 xnavi 前 64 名 0 组跳动 | 改为 **按 Run 分层采样**（每层 64 个、总量 384），第一轮全静止时再用 0x100 窄窗口对剩余候选复采一轮 |

修复后实测：`采样 384/1313 组，61 组在跳动` → 首次扫描即锁 `0x72EB58E310 (-572.9, 191.3, 1694.4)`，走动仲裁一次通过。

排查这类问题的通用方法：用 `_probe/active_all.py <pid> <x> <y> <z>` 做不限距离的全内存活跃扫描，
对比「xnavi 说全静止」和「实际有多少组在跳」，就能立刻区分是锚点错、采样漏，还是世界真的没在跑。

## 10. 换档后宝箱/装备/任务显示「已完成」（2026-11-05 修复）

现象：新开大师模式档后，地图上大片宝箱显示为已开启，但存档里一个都没开。

architectural 前提：**宝箱是否已开由前端判定，exe 不参与**。exe 只负责把 `game_data.sav` 二进制经
`/save` 交给浏览器，`app/js/save_upload.js` 解析 flag、`app/js/chest.js` 决定画不画成已开。
所以这类问题改前端即可，与地图瓦片数据无关。

| 层次 | 根因 | 处理 |
| --- | --- | --- |
| ① 注入被跳过 | 三处存档同步都写成「非空才覆盖」：`if (chestPts.length && ...)`。新档 0 宝箱 → 直接跳过 → localStorage 里 `botwmap.chestSaveDone.v1` 的上一档集合原样生效 | 改为空集也注入（整集替换） |
| ② 换档无感知 | 缓存键只按「宝箱」存，不区分存档来源，换档后没人清 | 加 `PlayTime` 指纹（`botwmap.savePlaytime.v1`）：时长回退 >30s 判定换档，自动清宝箱/装备/任务快照 |
| ③ 清理入口缺失 | `save_upload.js` 一直在 `getElementById('btnClearDone')`，但 `index.html` 里从来没这个元素（上游删了 DOM、留了 JS） | 补 🧹 图标按钮 |
| ④ 旧 JS 假象 | 静态资源无缓存头，浏览器自行缓存 `app/js/*.js`，代码改了刷新仍跑旧版 | 静态资源统一 `Cache-Control: no-cache`（304 重验证）；脚本 URL 加版本号 |

`Ctrl+F5` 清不掉这个问题，因为它清的是 HTTP 缓存，而残留数据在 **localStorage**。手动清理：

```js
['botwmap.done.v1','botwmap.chestDone.v1','botwmap.chestSaveDone.v1','botwmap.equipDone.v1',
 'botwmap.equipSaveDone.v1','botwmap.questDone.v1','botwmap.questSave.v1'].forEach(k=>localStorage.removeItem(k));
```

**发布注意**：本 fork 的前端修复位于 `app/`，而 `app/` 不打进 exe。发布包必须把 `app/` 一起打包，
否则用户只拿到新 exe + 旧地图包，前端修复等于没发。当前 Release 已包含 `app/` 目录。
