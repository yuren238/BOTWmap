# 旷野之息 · 互动地图

**[English](README_EN.md) | 简体中文**

《塞尔达传说：旷野之息》简体中文多图层互动地图 · 收集进度追踪 · 实时角色导航

![最新版本](https://img.shields.io/github/v/release/yalincc/BOTWmap?style=flat-square)　![在线版](https://img.shields.io/badge/在线版-botw.yalin.site-9a7b4f)　![PC 移植版](https://img.shields.io/badge/适配-PC%20移植版(Savage%20Wind)-d97706)

![地图界面](https://botw.yalin.site/media/readme-hero-v2.jpg)

> 🎮 **本仓库重点适配 PC 移植版：Savage Wind Recomp**（《旷野之息》Switch 1.9.0 的原生静态重编译 PC 版）。不用模拟器、不用装 Cemu/Ryujinx，直接跑 PC 原生程序；xnavi 为此实现了**静态指针链直读**——启动即定位，死亡复活 / 传送 / 过图自动跟随，无需走动确认。

## 这是什么

全中文互动地图：所有标注点官方简体中文命名；多图层同显、收集进度追踪、自定义标记；可选配 **xnavi** 在游戏进行时实时显示角色位置（模拟器三平台 + **PC 移植版 Savage Wind Recomp**）。纯静态页面，打开即用，无需服务器、无需安装。

## 直接使用

| 方式 | 操作 |
|---|---|
| **在线版** | 打开 <https://botw.yalin.site/> |
| 本地使用 | 浏览器打开 `app/index.html`（瓦片 / 图标 / 数据全部本地，断网可用） |

完成进度、图层选择、自定义标记自动保存在浏览器本地，关页不丢。

## 实时角色导航（可选）

游戏时想在地图上看到自己当前位置？配合 **xnavi**（支持 Cemu / Ryujinx / Eden，以及原生重编译版 Savage Wind Recomp）：

![xnavi 定位界面](https://botw.yalin.site/media/xnavi-gui-hero-v2.jpg)

1. 启动模拟器并进入游戏
2. 运行 `xnavi.exe`，点击「开始定位」
3. 打开在线地图，右下角显示实时坐标
4. 手机/平板跟随（可选）：与电脑同一 WiFi，点地图右下角 📱 扫码，手机浏览器打开即当第二屏（红点跟随 / 收集进度同步）；瓦片已内置在本仓库 `app/assets/tiles`（10096 张，z1~z7，68MB），断网可用

- 四平台自动识别（Cemu / Ryujinx / Eden / **Savage Wind PC 移植版**），无需手选；仅读内存，不注入、不改游戏
- 存档联动：游戏保存后 ≤2 秒地图进度自动刷新
- 下载与使用说明见 [Releases](https://github.com/yuren238/BOTWmap/releases)（本仓库适配版 tag 为 `v3.2.2-savagewind.*`；原版见[上游](https://github.com/yalincc/BOTWmap/releases)）

### PC 移植版 Savage Wind Recomp（本仓库主打适配）

[Savage Wind Recomp](https://github.com/Revivedx/SavageWindRecomp) 是《旷野之息》Switch 1.9.0 的**静态重编译原生 PC 版**（static recompilation）：游戏代码被翻译成 PC 原生程序直接运行，**不是模拟器、不需要任何模拟器环境**。地图与点位完全复用，差别只在定位程序：

```bat
xnavi.exe --emu=savagewind
REM 存档默认从 <游戏目录>\save\save_0 读取；自定义： --savagewind-save-dir="...\save"
```

针对 PC 移植版专门做了三件事：

1. **静态指针链直读**：recomp 把 guest main 固定映射在 `0x7000000000`，其中一组全局指针由游戏自身维护并指向玩家对象。xnavi 标定出 18 条链，**启动即定位、免扫描免走动确认**（`verified` 启动即真），每 tick 仅 36 次微量读取代 3GB 全内存扫描
2. **死亡 / 传送 / 过图天然跟随**：指针由游戏自己每帧更新，红点不会钉死；实测死亡画面部分链清零、复活 4 秒内 18/18 恢复
3. **版本指纹护栏**：游戏或 recomp 更新后若 guest 布局变化，三层护栏（模块基址 / 指针域 / 结构指纹）让链**显式失效并自动落回动态扫描**，绝不静默给错坐标

适配细节、实测数据与已知限制见 [xnavi/ADAPT-SAVAGEWIND.md](xnavi/ADAPT-SAVAGEWIND.md)。

开箱即用的 Windows 包：[**最新 Release**](https://github.com/yuren238/BOTWmap/releases/latest)（当前 `v3.2.2-savagewind.7`）
（`Xnavi.exe` GUI + `xnavi-core.exe` 定位核心，解压后两个 exe 放同一目录，平台下拉选 **Savage Wind (PC移植版)**）。

## 核心功能

| 功能 | 说明 |
|---|---|
| 多图层同显 | 标注层任意勾选组合，一键全选 / 清空；探索收集 / 设施 / 敌人 / 任务四组分类 + 装备 / 宝箱 / 营地 / 材料独立图层 |
| 高清瓦片 | 7 级动态瓦片按需加载，最大 1:1 像素仍清晰，拖动缩放流畅 |
| 点位详情 | 点击任意标注：中文名 / 类别 / 区域 / 塔域 / 游戏坐标 / 位置提示 |
| 搜索 | 中英双语、按区域 / 塔名搜索，回车直达并居中放大 |
| 收集追踪 | 标记完成自动统计完成度（神庙 / 塔 / 神兽 / 克洛格 / 回忆 / 宝箱 / 任务）；可上传存档自动同步 |
| 材料追踪 | 74 种材料 / 13524 个采集点，独立 Tab + 专用搜索 |
| 营地刷点 | 555 个血月刷新营地，标注掉落武器，回答「去哪刷什么武器」 |
| 任务追踪 | 官方三档任务全 147 项，完成状态存档同步 + 手动标记双源 |
| 装备图鉴 | 武器 90 + 防具 31，收录来源与获取点，可逐个定位 |
| 存档修改器 | 网页端改存档：卢比 / 心 / 耐力 / 克洛格、装备耐久与封印属性、材料与料理数量；另附 Switch ↔ Wii U 存档互转（测试中）。全程离线，不上传、不写盘 |
| 常用工具 | 距离测量 / 自定义标记 / 分享链接 / 坐标读数 / 龙飞行轨迹 |
| PC 移植版适配 | Savage Wind Recomp 专属定位：静态指针链直读，启动即定位、死亡/传送自动跟随；另有 Cemu / Ryujinx / Eden 三平台模拟器支持 |

## 收录内容

28 类点位 3000+：神庙 137（含 DLC）、希卡塔 15、神兽 4、克洛格果实 900、宝箱 1315、敌人 3000+（含大师模式分层）、任务 147、装备 121、材料 74 种 13524 刷点、营地 555、龙飞行轨迹 3 条。

## 已知限制

- 部分神庙试炼信息缺失（祝福型神庙等）
- 区域为按官方区域中心点的近似归属，个别边界点可能偏差
- 少量冷门物品保留英文名（如大师模式专属变体）

## 版本

网页版 **v1.7.5**；导航程序 xnavi 版本独立（本适配版 Latest：**`v3.2.2-savagewind.7`**，见 [Releases](https://github.com/yuren238/BOTWmap/releases)；原版见[上游](https://github.com/yalincc/BOTWmap/releases)）。更新明细见 [CHANGELOG.md](CHANGELOG.md)。

## 免责声明

地图数据与素材来自游戏本体（romfs）及社区公开资料，游戏内容版权归任天堂（Nintendo）所有；本项目为学习交流用途，与任天堂无关。代码部分以 MIT 协议开源，见 [LICENSE](LICENSE)。
