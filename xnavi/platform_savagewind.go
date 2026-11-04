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
	"os"
	"path/filepath"
	"strings"
)

// guest 地址空间下界：Switch 用户态地址从 0x7000000000 起（日志实测模块基址）。
// 用它把 host 侧的 DLL/堆分配挡在外面，避免把宿主内存当游戏内存扫。
const savageWindGuestBase = uintptr(0x7000000000)

type savageWindPlatform struct {
	h   uintptr
	pid uint32
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

func (p *savageWindPlatform) Handle() uintptr   { return p.h }
func (p *savageWindPlatform) PID() uint32       { return p.pid }
func (p *savageWindPlatform) LittleEndian() bool { return true }

// Blocks 返回 guest 地址空间内已提交可读写、且不小于 minMB 的区域。
// 实测：3121MB（guest RAM）+ 数个 200~360MB（纹理/资源堆）都落在 0x7000000000+。
func (p *savageWindPlatform) Blocks(minMB float64) []MemBlock {
	return savageWindRWBlocks(p.h, minMB)
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
	if p.h != 0 {
		exe = processExePath(p.h)
	} else if p.pid != 0 {
		exe = processExePathByPID(p.pid)
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
