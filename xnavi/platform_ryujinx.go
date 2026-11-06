// platform_ryujinx.go — Ryujinx 平台驱动。
//
// 基线：BotwNavi（live-go）。差异点：
//   - 进程：前缀匹配 "ryujinx"（进程名多变：Ryujinx / Ryujinx.Ava）
//   - 内存：guest DRAM 表现为 MEM_MAPPED 提交区（有 32GB 高的镜像块需剔除）
//   - 字节序：Switch = 小端
//   - 存档：%APPDATA%\Ryujinx\bis\user\save；主档 = 最高 playtime 槽

package main

import (
	"os"
	"path/filepath"
	"sync"
)

// ryujinxPlatform 的 h/pid 是共享单例状态：tracker 主循环（Attach/Handle）与
// 平台探测/看门狗 goroutine（probePlatform/platformWithGame）会并发访问，
// 必须持锁，否则可能出现双重 closeHandle / 半开句柄 / 撕裂写。
type ryujinxPlatform struct {
	mu  sync.Mutex
	h   uintptr
	pid uint32
}

// snapshot 原子取 (h, pid)；读侧拿到过期句柄由 vqRetry 等既有失败路径自愈。
func (p *ryujinxPlatform) snapshot() (uintptr, uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h, p.pid
}

func init() {
	registerPlatform(&ryujinxPlatform{})
}

func (p *ryujinxPlatform) Name() string { return "Ryujinx" }

func (p *ryujinxPlatform) Attach() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pid := findPid("ryujinx")
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
		p.h = 0 // 句柄已失效（进程曾退出/被替换），继续走重开
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

func (p *ryujinxPlatform) Handle() uintptr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h
}
func (p *ryujinxPlatform) PID() uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid
}
func (p *ryujinxPlatform) LittleEndian() bool { return true }

// Blocks guest DRAM：MEM_MAPPED 提交区（剔除 32GB 高的镜像块）。
func (p *ryujinxPlatform) Blocks(minMB float64) []MemBlock {
	h, _ := p.snapshot()
	return ryuGuestBlocks(h, minMB)
}

// LargestBlock 最大 guest DRAM（known 偏移重定位锚点）。
func (p *ryujinxPlatform) LargestBlock(minMB float64) (uintptr, uintptr) {
	h, _ := p.snapshot()
	return ryuLargestGuestBlock(h, minMB)
}

// ryujinxSaveDirOverride 由 --ryujinx-save-dir= 指定（GUI 设置"Ryujinx 存档路径"），
// 直接作为存档根，优先于一切自动探测。
var ryujinxSaveDirOverride string

func setRyujinxSaveDirOverride(d string) { ryujinxSaveDirOverride = d }

// SaveRoots Ryujinx 存档根（Switch 版，优先级：手动指定 > 进程自省 > AppData）。
func (p *ryujinxPlatform) SaveRoots() []string {
	// P0: 手动指定（最高优先级）
	if sd := ryujinxSaveDirOverride; sd != "" {
		return []string{sd}
	}
	var roots []string
	add := func(pp string) {
		if pp != "" {
			roots = append(roots, filepath.Clean(pp))
		}
	}

	// P1: 进程自省（提取 ryuDir，检查 ryuDir/portable/bis/user/save）
	var ryuExe string
	h, pid := p.snapshot()
	if h != 0 {
		ryuExe = processExePath(h)
	} else if pid != 0 {
		ryuExe = processExePathByPID(pid)
	}
	if ryuExe != "" {
		ryuDir := filepath.Dir(ryuExe)
		add(filepath.Join(ryuDir, "portable", "bis", "user", "save"))
		add(filepath.Join(ryuDir, "bis", "user", "save"))
	}

	// P2: 检查 %APPDATA%/Ryujinx/bis/user/save
	appdata := os.Getenv("APPDATA")
	if appdata == "" {
		appdata = os.Getenv("USERPROFILE") + "\\AppData\\Roaming"
	}
	if appdata != "" {
		add(filepath.Join(appdata, "Ryujinx", "bis", "user", "save"))
	}
	return roots
}

// ---- 内存枚举（Ryujinx 专属，原 live-go winapi.go）----

// ryuGuestBlocks 枚举所有足够大的 RW MEM_MAPPED 提交区域（去掉 32GB 高的镜像块）。
func ryuGuestBlocks(h uintptr, minMB float64) []MemBlock {
	minSize := uintptr(minMB * 1048576.0)
	var raw []MemBlock
	baseSet := map[uintptr]bool{}
	addr := uintptr(0)
	const limit = uintptr(0x7FFFFFFFFFFF)
	const mirrorStep = uintptr(0x800000000)
	consecFails := 0
	for addr < limit {
		var mbi MemoryBasicInformation
		if !vqRetry(h, addr, &mbi) {
			consecFails++
			if consecFails > 64 {
				break // 句柄失效等持续失败：放弃本轮，避免空转
			}
			addr += 0x1000
			continue
		}
		consecFails = 0
		base, size := mbi.BaseAddress, mbi.RegionSize
		p := mbi.Protect & 0xFF
		if mbi.State == memCommit && mbi.Type == memMapped &&
			(p == 0x02 || p == 0x04) {
			if size >= minSize {
				raw = append(raw, MemBlock{base, size})
				baseSet[base] = true
			}
		}
		nxt := base + size
		if nxt > addr {
			addr = nxt
		} else {
			addr += 0x1000
		}
	}
	var out []MemBlock
	for _, b := range raw {
		if !baseSet[b.Base+mirrorStep] {
			out = append(out, b)
		}
	}
	return out
}

// ryuLargestGuestBlock 返回最大的一块 guest DRAM（供 known offsets 重定位）。
// 过滤镜像块：和 ryuGuestBlocks 一样排除 base+0x800000000 也存在的块。
func ryuLargestGuestBlock(h uintptr, minMB float64) (uintptr, uintptr) {
	const mirrorStep = uintptr(0x800000000)
	type block struct{ base, size uintptr }
	var candidates []block
	addr := uintptr(0)
	const limit = uintptr(0x7FFFFFFFFFFF)
	consecFails := 0
	for addr < limit {
		var mbi MemoryBasicInformation
		if !vqRetry(h, addr, &mbi) {
			consecFails++
			if consecFails > 64 {
				break // 句柄失效等持续失败：放弃本轮，避免空转
			}
			addr += 0x1000
			continue
		}
		consecFails = 0
		base, size := mbi.BaseAddress, mbi.RegionSize
		if mbi.State == memCommit && mbi.Type == memMapped &&
			((mbi.Protect&0xFF) == 0x02 || (mbi.Protect&0xFF) == 0x04) &&
			(mbi.Protect&pageGuard) == 0 && size >= uintptr(minMB*1048576.0) {
			candidates = append(candidates, block{base, size})
		}
		nxt := base + size
		if nxt > addr {
			addr = nxt
		} else {
			addr += 0x1000
		}
	}
	for _, c := range candidates {
		isMirror := false
		for _, c2 := range candidates {
			if c2.base == c.base+mirrorStep {
				isMirror = true
				break
			}
		}
		if !isMirror {
			return c.base, c.size
		}
	}
	if len(candidates) > 0 {
		return candidates[0].base, candidates[0].size
	}
	return 0, 0
}
