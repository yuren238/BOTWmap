// platform_savagewind.go — Savage Wind Recomp 平台驱动。
//
// Savage Wind（https://github.com/Revivedx/SavageWindRecomp）是《旷野之息》
// Switch 1.9.0 的静态重编译（static recompilation）原生 PC 版，不是模拟器：
//   - 进程：savagewind.exe（同一目录还有 "Savage Wind Configuration.exe"，前缀不匹配）
//   - 内存：游戏模块按 **guest 地址原地映射**（日志 [gamelib] loaded main at 7000000000），
//     3GB guest RAM 直接作为进程内一块已提交 RW 私有区域出现（实测 0x723FF00000 起 ~3121MB）。
//     因此不需要模拟器的地址翻译：guest 偏移 = 进程偏移，小端、与 Switch 一致。
//   - 字节序：Switch = 小端
//   - 存档：<游戏目录>/save/save_0/<槽位>/game_data.sav（Switch 原生格式，同样大小 1027248）
//     槽位 0..5 轮转写，槽号不可靠 → 主档按 mtime 最新（与 Cemu/Eden 同策略）
//
// 与其它平台的关键差异：
//   - 无固定已知偏移（社区偏移只对 Cemu/Ryujinx 版本成立），首次定位走动态扫描，
//     锁定后把"相对最大块基址的偏移"写入 known_botw_savagewind.json，后续秒锁。
//   - guest RAM 在 guest 地址空间（0x7000000000+），模块代码区是 RX，
//     所以"RW + 地址落在 guest 空间 + 足够大"三条即可稳定筛选出游戏内存块。

package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// guest 地址空间下界：Switch 用户态地址从 0x7000000000 起（日志实测模块基址）。
// 用它把 host 侧的 DLL/堆分配挡在外面，避免把宿主内存当游戏内存扫。
const savageWindGuestBase = uintptr(0x7000000000)

// h/pid 为共享单例状态（tracker 主循环与平台探测/看门狗 goroutine 并发访问），
// 读写必须持锁：否则可能双重 closeHandle / 半开句柄 / 撕裂写。
type savageWindPlatform struct {
	mu  sync.Mutex
	h   uintptr
	pid uint32
}

func (p *savageWindPlatform) snapshot() (uintptr, uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h, p.pid
}

// savageWindSaveDirOverride 由 --savagewind-save-dir= 指定（手动指定最高优先级）。
var savageWindSaveDirOverride string

func setSavageWindSaveDirOverride(d string) { savageWindSaveDirOverride = d }

func init() {
	registerPlatform(&savageWindPlatform{})
}

func (p *savageWindPlatform) Name() string { return "SavageWind" }

// findSavageWindPid 精确匹配进程名 savagewind（不含 Configuration 壳）。
func findSavageWindPid() uint32 {
	for _, pid := range findPids("savagewind") {
		return pid
	}
	return 0
}

func (p *savageWindPlatform) Attach() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pid := findSavageWindPid()
	if pid == 0 {
		if p.h != 0 {
			closeHandle(p.h)
		}
		p.h, p.pid = 0, 0
		return false
	}
	if pid == p.pid && p.h != 0 {
		if hLive(p.h) {
			return true
		}
		closeHandle(p.h)
		p.h = 0
	}
	if p.h != 0 {
		closeHandle(p.h)
	}
	h, err := openProcess(pid)
	if err != nil || h == 0 {
		p.h, p.pid = 0, 0
		return false
	}
	p.h, p.pid = h, pid
	return true
}

func (p *savageWindPlatform) Handle() uintptr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}
func (p *savageWindPlatform) PID() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid
}
func (p *savageWindPlatform) LittleEndian() bool { return true }

// Blocks 返回 guest 地址空间内已提交可读写、且不小于 minMB 的区域。
// 实测：3121MB（guest RAM）+ 数个 200~360MB（纹理/资源堆）都落在 0x7000000000+。
func (p *savageWindPlatform) Blocks(minMB float64) []MemBlock {
	h, _ := p.snapshot()
	return savageWindRWBlocks(h, minMB)
}

// LargestBlock 最大 guest RW 块（known 偏移锚点）。
func (p *savageWindPlatform) LargestBlock(minMB float64) (uintptr, uintptr) {
	var best MemBlock
	for _, b := range p.Blocks(minMB) {
		if b.Size > best.Size {
			best = b
		}
	}
	return best.Base, best.Size
}

// SaveRoots 存档根（优先级：手动指定 > 进程 exe 同目录 save/）。
// Savage Wind 把 "game files, saves and settings" 都放在程序目录，
// 因此 <exe dir>/save 就是存档根，下面有 save_0/<0..5>/game_data.sav。
func (p *savageWindPlatform) SaveRoots() []string {
	if sd := savageWindSaveDirOverride; sd != "" {
		return []string{filepath.Clean(sd)}
	}
	var roots []string
	exe := ""
	h, pid := p.snapshot()
	if h != 0 {
		exe = processExePath(h)
	} else if pid != 0 {
		exe = processExePathByPID(pid)
	}
	if exe != "" {
		dir := filepath.Dir(exe)
		roots = append(roots, filepath.Join(dir, "save"))
		// 兼容"把数据目录放到别处"（docs/data-folder.md）：save 可能在上一级。
		roots = append(roots, filepath.Join(filepath.Dir(dir), "save"))
	}
	// Linux 约定（本仓 Windows 构建用不到，保留以免 fork 到 Linux 时找不到）。
	if home := os.Getenv("HOME"); home != "" {
		roots = append(roots, filepath.Join(home, ".local", "share", "savagewind", "save"))
	}
	return roots
}

// savageWindRWBlocks 枚举 guest 地址空间内的 RW 提交区域（按 AllocationBase 去重）。
func savageWindRWBlocks(h uintptr, minMB float64) []MemBlock {
	minSize := uintptr(minMB * 1048576.0)
	var out []MemBlock
	seen := map[uintptr]bool{}
	addr := savageWindGuestBase
	const limit = uintptr(0x8000000000)
	consecFails := 0
	for addr < limit {
		var mbi MemoryBasicInformation
		if !vqRetry(h, addr, &mbi) {
			consecFails++
			if consecFails > 64 {
				break // 句柄失效：放弃本轮，避免空转
			}
			addr += 0x1000
			continue
		}
		consecFails = 0
		base, size := mbi.BaseAddress, mbi.RegionSize
		prot := mbi.Protect & 0xFF
		if mbi.State == memCommit && (prot == 0x02 || prot == 0x04) &&
			(mbi.Protect&pageGuard) == 0 && size >= minSize && !seen[base] {
			seen[base] = true
			out = append(out, MemBlock{base, size})
		}
		nxt := base + size
		if nxt > addr {
			addr = nxt
		} else {
			addr += 0x1000
		}
	}
	return out
}

// savageWindExeDirGuess 进程没跑时给日志/文档用的提示路径（不参与定位）。
func savageWindExeDirGuess() string {
	if exe := processExePathByPID(findSavageWindPid()); exe != "" {
		return filepath.Dir(exe)
	}
	return ""
}

// isSavageWindSavePath 判断某存档路径是否落在 Savage Wind 目录结构里（save_0 特征）。
func isSavageWindSavePath(p string) bool {
	low := strings.ToLower(filepath.ToSlash(p))
	return strings.Contains(low, "/save/save_0/")
}

// ---------------------------------------------------------------------------
// 静态指针链（2026-11-06 实测标定，BOTW 1.9.0 + Savage Wind 0.4.1-alpha）
//
// Savage Wind recomp 把 guest main 模块固定映射在 0x7000000000（savagewind.log:
// "loaded main at 7000000000 size 1e98000"），main 的 .data/.bss 因此位于
// host 进程的固定地址，跨重启不变。其中一组全局指针由游戏自身维护，指向持有
// 玩家坐标的对象——读链就是游戏自己的寻址方式：
//   - 每帧重新解引用，无需锁定地址；
//   - 死亡复活 / 传送 / 过图后游戏自己更新指针，天然跟随（实测死亡画面部分
//     链短暂失效、复活后 4 秒内 18/18 全部恢复并指向新对象）；
//   - 每 tick 36 次微量读，取代 3GB 全内存海选。
//
// 标定过程：以最新存档锚点为中心扫 guest RAM 收集坐标候选，差分走动前后定位
// 真身镜像群（1851 个），再在 main 静态区（32MB）反向扫描指向镜像附近
// [ptr, ptr+0x400] 的指针得到 18 条链，逐一验证走动跟随与死亡复活存活。
//
// 若游戏版本/recomp 布局变化导致链条失效（指针越界 / 坐标非法 / 无多数派），
// readSavageWindChains 返回 0 票，上层自动落回原有动态扫描管道。
// ---------------------------------------------------------------------------

type swStaticChain struct {
	static uintptr // main .data/.bss 中的全局指针槽地址（跨重启不变）
	off    uintptr // 玩家坐标在该指针所指对象内的偏移
}

// savageWindChainPrimary 主链：+0x80 偏移与逆向确认的玩家对象布局一致
// （0x40 步长环缓冲、位置+单位四元数），死亡全程未失效。
const savageWindChainPrimary = 0

var savageWindStaticChains = []swStaticChain{
	{0x7001DC0A38, 0xF0},
	{0x7001E0D040, 0x58},
	{0x7001E0D0C8, 0x60},
	{0x7001E12870, 0x270},
	{0x7001E12878, 0x270},
	{0x7001E13EC8, 0x80},
	{0x7001E13ED0, 0x88},
	{0x7001E15300, 0x218},
	{0x7001E15308, 0x218},
	{0x7001E327D8, 0x68},
	{0x7001E328D8, 0x80},
	{0x7001E396E8, 0xD8},
	{0x7001E39700, 0xD8},
	{0x7001E3F570, 0x1C0},
	{0x7001E3F578, 0x1C8},
	{0x7001E95488, 0x174},
	{0x7001E954E8, 0x17C},
	{0x7001E957A8, 0x2AC},
}

// ---------------------------------------------------------------------------
// 指针链护栏（版本/布局指纹）
//
// 链按 BOTW 1.9.0 + Savage Wind 0.4.1-alpha.4 标定。游戏/recomp 更新后若 guest
// 布局变化，静态槽最危险的不是失效（0 票自动落回海选），而是仍解出"看起来
// 合法"的坐标——例如槽位被复用后指向同结构体类型的 NPC，投票照样过半，
// 红点静默指错。三层护栏把静默错位变成显式失效（该链弃权）：
//   G1 模块指纹：main 必须仍按标定布局映射在 0x7000000000（已提交、可执行）
//   G2 指针域：  链解出的对象指针必须落在 main 模块之外（活对象在 guest 堆；
//                解出模块内地址 = .data 布局已变的强信号）
//   G3 结构指纹：对象内 [坐标, 坐标+0x30) 必须是"合法三元组 + 正交旋转矩阵"
//                （与标定时结构扫描的门禁同款，见 locate.go rotOKF）
// G1 失败 = 布局已变，全链直接失效（0 票）落回动态扫描管道。
// ---------------------------------------------------------------------------

const (
	swModuleBase = uintptr(0x7000000000)  // guest main 固定映射基址
	swModuleSize = uintptr(0x1e98000)     // savagewind.log: "loaded main at 7000000000 size 1e98000"
	swGuestHi    = uintptr(0x8000000000)  // Switch 用户态上界
)

// savageWindChainCalVersion 链标定针对的游戏版本（仅用于日志指纹比对）。
const savageWindChainCalVersion = "0.4.1-alpha.4"

var (
	swModuleMu     sync.Mutex
	swModuleHandle uintptr
	swModuleOKVal  bool
)

// savageWindModuleOK G1 模块指纹：main 是否仍映射在标定基址（按句柄缓存，每 tick 至多一次 VQ）。
// 实测（2026-11-06 savagewind.log + VQ 观察）：recomp 把 guest main 按 guest 地址
// **原地映射为一块 RW 私有数据区（Protect=0x04）**——静态重编译的原生代码不从这里
// 执行，guest 指令只是数据。因此指纹 = "0x7000000000 起有一块已提交区"，不检查
// 可执行位（检查了反而误杀）。
func savageWindModuleOK(h uintptr) bool {
	swModuleMu.Lock()
	defer swModuleMu.Unlock()
	if swModuleHandle == h {
		return swModuleOKVal
	}
	ok := true
	why := "通过"
	var mbi MemoryBasicInformation
	switch {
	case !vqRetry(h, swModuleBase, &mbi):
		ok, why = false, "0x7000000000 查询失败（地址未映射）"
	case mbi.State != memCommit:
		ok, why = false, fmt.Sprintf("基址区未提交 State=0x%X", mbi.State)
	case mbi.BaseAddress != swModuleBase:
		ok, why = false, fmt.Sprintf("基址区起点偏移 Base=0x%X", mbi.BaseAddress)
	case mbi.Protect&0xFF == 0:
		ok, why = false, fmt.Sprintf("基址区保护位异常 Protect=0x%X", mbi.Protect)
	}
	swModuleHandle, swModuleOKVal = h, ok
	if !ok {
		fmt.Printf("  [chain] 指纹护栏 G1：guest main 检查未过（%s）-> 静态链全链失效，落回动态扫描 / Module fingerprint mismatch (%s), chains disabled\n", why, why)
	}
	return ok
}

// swChainSampleOK G3 结构指纹：48 字节对象样本应为"合法坐标三元组 + 正交旋转矩阵"。
func swChainSampleOK(cb []byte) ([3]float32, bool) {
	if len(cb) < 48 {
		return [3]float32{}, false
	}
	x := math.Float32frombits(binary.LittleEndian.Uint32(cb[0:]))
	y := math.Float32frombits(binary.LittleEndian.Uint32(cb[4:]))
	z := math.Float32frombits(binary.LittleEndian.Uint32(cb[8:]))
	if !WorldCoordOK(x, y, z) {
		return [3]float32{}, false
	}
	var m [9]float32
	for i := 0; i < 9; i++ {
		m[i] = math.Float32frombits(binary.LittleEndian.Uint32(cb[12+i*4:]))
	}
	if !rotOKF(m[:]) {
		return [3]float32{}, false
	}
	return [3]float32{x, y, z}, true
}

// readSavageWindChains 读取全部静态链并投票。
// 主链有效时直接采用主链坐标（玩家本体），其余链做佐证计数；
// 主链失效（死亡画面等过渡期）时对剩余链做 8m 桶多数派投票兜底。
// 返回 (坐标, 有效票数, 主坐标地址)；票数 < 2 视为链不可用。
func readSavageWindChains(h uintptr) ([3]float32, int, uintptr) {
	// G1 模块指纹：布局已变时全链直接失效
	if !savageWindModuleOK(h) {
		return [3]float32{}, 0, 0
	}

	type hit struct {
		pos  [3]float32
		addr uintptr
	}
	hits := make([]hit, 0, len(savageWindStaticChains))
	for _, ch := range savageWindStaticChains {
		buf := readMem(h, ch.static, 8)
		if len(buf) != 8 {
			continue
		}
		ptr := uintptr(binary.LittleEndian.Uint64(buf))
		// G2 指针域：活对象在 guest 堆，解出模块内地址 = 布局已变
		if ptr < swModuleBase+swModuleSize || ptr >= swGuestHi {
			continue
		}
		cb := readMem(h, ptr+ch.off, 48)
		if len(cb) != 48 {
			continue
		}
		pos, ok := swChainSampleOK(cb)
		if !ok {
			continue
		}
		hits = append(hits, hit{pos, ptr + ch.off})
	}
	if len(hits) < 2 {
		return [3]float32{}, 0, 0
	}

	// 主链优先：重读主链指针确定其坐标地址，与 hits 比对
	primary := savageWindStaticChains[savageWindChainPrimary]
	var primaryAddr uintptr
	if pb := readMem(h, primary.static, 8); len(pb) == 8 {
		if p := uintptr(binary.LittleEndian.Uint64(pb)); p >= swModuleBase+swModuleSize && p < swGuestHi {
			primaryAddr = p + primary.off
		}
	}
	var primaryPos [3]float32
	havePrimary := false
	for _, hh := range hits {
		if hh.addr == primaryAddr {
			primaryPos = hh.pos
			havePrimary = true
			break
		}
	}
	if havePrimary {
		n := 0
		for _, hh := range hits {
			if dist3(hh.pos, primaryPos) < 100.0 {
				n++
			}
		}
		if n >= 2 {
			return primaryPos, n, primaryAddr
		}
	}

	// 多数派兜底：8m 桶聚类取最大簇，逐轴中位数
	type key struct{ x, z int }
	buckets := make(map[key][]hit)
	for _, hh := range hits {
		k := key{int(hh.pos[0] / 8), int(hh.pos[2] / 8)}
		buckets[k] = append(buckets[k], hh)
	}
	var best []hit
	for _, v := range buckets {
		if len(v) > len(best) {
			best = v
		}
	}
	if len(best) < 2 {
		return [3]float32{}, 0, 0
	}
	xs := make([]float32, 0, len(best))
	ys := make([]float32, 0, len(best))
	zs := make([]float32, 0, len(best))
	for _, hh := range best {
		xs = append(xs, hh.pos[0])
		ys = append(ys, hh.pos[1])
		zs = append(zs, hh.pos[2])
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	sort.Slice(ys, func(i, j int) bool { return ys[i] < ys[j] })
	sort.Slice(zs, func(i, j int) bool { return zs[i] < zs[j] })
	med := func(a []float32) float32 { return a[len(a)/2] }
	return [3]float32{med(xs), med(ys), med(zs)}, len(best), best[0].addr
}
