package main

// chain_funnel.go — Savage Wind「结构漏斗」：静态指针链失效时的现场重建（L1 层）
//
// 移植自上游 yalincc/BOTWmap xnavi-pc/chain.go 的 resolveByStaticSlots
// （提交 fbc2661，2026-11-07 实测验证后接入）。
//
// 判据（本机实测：49.3MB 静态区 / 306k 出站指针 / 137k 目标 →
// 坐标合法 22 → 自指 1，唯一命中 obj，槽位与本项目链表 #12/#13 完全一致）：
//
//	漏斗① obj+0xD8  = 合法世界坐标（严格口径：|x|、|z| 近似 0 秒拒，
//	                  排除 (0, 0, z) 轴向量假命中；denormal 秒拒）
//	漏斗② obj+0x120 = 自指（intrusive list 头，值 == obj+0x120）
//
// 分层定位：
//
//	L0  18 条标定链直读        ~微秒   常态路径（游戏原生寻址）
//	L1  结构漏斗现场重建        ~200-400ms  L0 票数不足时（游戏更新/recomp 布局变化）
//	L2  全窗口海选 + 走动确认   ~3s     L1 也失败时（漏斗判据同样不符）
//
// L1 命中后把重建出的槽位写入学习缓存，后续 L0 直接直读，不必再扫。

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

const (
	swPosFieldOff = uintptr(0xD8)  // 玩家对象内世界坐标偏移
	swSelfRefOff  = uintptr(0x120) // 自指字段偏移（intrusive list 头）
	swFunnelChunk = 4 << 20        // 扫描分块大小
	swFunnelEvery = 3 * time.Second
	swStaticScanSpan = 512 << 20 // 静态区/类型指针有效范围（上游同口径）
)

// swTripleStrict 严格世界坐标判据（上游 validTriple 同口径）。
// 比 WorldCoordOK 多三条（实测把 156 → 22，是漏斗唯一性的关键）：
//   - |x| < 1e-20 或 |z| < 1e-20 秒拒（(0,0,z) 轴向量、指针字节当 float 的典型形态）
//   - |x|、|z| 同时 < 5 秒拒（近原点噪声）
//   - 任一非零分量 < 1e-6 秒拒（denormal）
func swTripleStrict(x, alt, z float32) bool {
	if x != x || alt != alt || z != z {
		return false
	}
	if math.IsInf(float64(x), 0) || math.IsInf(float64(alt), 0) || math.IsInf(float64(z), 0) {
		return false
	}
	if x == 0 && alt == 0 && z == 0 {
		return false
	}
	ax, az := math.Abs(float64(x)), math.Abs(float64(z))
	if ax < 5 && az < 5 {
		return false
	}
	if (ax < 5 && float64(alt) < 5) || (az < 5 && float64(alt) < 5) {
		return false
	}
	if ax < 1e-20 || az < 1e-20 {
		return false
	}
	if x <= -7000 || x >= 7000 || z <= -7000 || z >= 7000 || alt <= -600 || alt >= 5000 {
		return false
	}
	if ax < 100 && az < 100 && float64(alt) < 100 {
		return false
	}
	for _, v := range [3]float32{x, alt, z} {
		if a := math.Abs(float64(v)); a != 0 && a < 1e-6 {
			return false
		}
	}
	return true
}

// swStaticPtrAt0 obj+0x00 是否为指向静态区的类型指针（上游 staticPtrAt0 判据）。
// 实测（2026-11-07）18/18 条标定链一致成立——"解出的地址确实是活对象头"的
// 通用硬判据，比"坐标碰巧合法"强得多：解到随机堆内存时几乎不可能命中。
func swStaticPtrAt0(h, obj uintptr) bool {
	b := readMem(h, obj, 8)
	if len(b) != 8 {
		return false
	}
	v := uintptr(binary.LittleEndian.Uint64(b))
	return v >= swModuleBase && v < swModuleBase+swStaticScanSpan
}

func swReadTriple(h, addr uintptr) ([3]float32, bool) {
	b := readMem(h, addr, 12)
	if len(b) != 12 {
		return [3]float32{}, false
	}
	return [3]float32{
		math.Float32frombits(binary.LittleEndian.Uint32(b[0:])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[4:])),
		math.Float32frombits(binary.LittleEndian.Uint32(b[8:])),
	}, true
}

// swModuleRWSpan guest main 模块内（[base, base+size)）的 RW 已提交区段。
// 实测：recomp 把整个 main 映像映射为一块 Protect=0x04 的私有数据区。
func swModuleRWSpan(h uintptr) []MemBlock {
	lo := swModuleBase
	hi := swModuleBase + swModuleSize
	var out []MemBlock
	for addr := lo; addr < hi; {
		var mbi MemoryBasicInformation
		if !vqRetry(h, addr, &mbi) {
			addr += 0x1000
			continue
		}
		base, size := mbi.BaseAddress, mbi.RegionSize
		if mbi.State == memCommit && (mbi.Protect&0xFF) == 0x04 && (mbi.Protect&pageGuard) == 0 {
			b, s := base, size
			if b < lo {
				s -= lo - b
				b = lo
			}
			if b+s > hi {
				s = hi - b
			}
			if s >= 0x1000 {
				out = append(out, MemBlock{b, s})
			}
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

// swFunnelHit 漏斗结果：玩家对象基址 + 静态区里指向它的前两个槽 + 坐标。
type swFunnelHit struct {
	obj   uintptr
	slots [2]uintptr
	pos   [3]float32
}

type swFunnelRef struct{ a, b uintptr }

// resolveSavageWindFunnel 静态区出站指针 → 坐标漏斗 → 自指漏斗，唯一命中即玩家对象。
func resolveSavageWindFunnel(h uintptr) (swFunnelHit, bool) {
	t0 := time.Now()
	blocks := swModuleRWSpan(h)
	if len(blocks) == 0 {
		fmt.Printf("  [funnel] 结构漏斗：静态区不可读，跳过 / Static span unreadable, funnel skipped\n")
		return swFunnelHit{}, false
	}

	// 1) 分块多线程扫出站指针 → 目标地址 → 前两个引用槽
	var jobs []MemBlock
	for _, b := range blocks {
		for off := uintptr(0); off < b.Size; off += swFunnelChunk {
			n := uintptr(swFunnelChunk)
			if off+n > b.Size {
				n = b.Size - off
			}
			jobs = append(jobs, MemBlock{b.Base + off, n})
		}
	}
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		ptrCnt  int
		refs    = make(map[uintptr]swFunnelRef, 1<<17)
		jobCh   = make(chan MemBlock)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make(map[uintptr]swFunnelRef, 1<<14)
			n := 0
			for j := range jobCh {
				buf := readMem(h, j.Base, int(j.Size))
				for i := 0; i+8 <= len(buf); i += 8 {
					v := uintptr(binary.LittleEndian.Uint64(buf[i:]))
					if v < swModuleBase || v >= swGuestHi {
						continue
					}
					n++
					at := j.Base + uintptr(i)
					e := local[v]
					if e.a == 0 {
						e.a = at
					} else if e.b == 0 {
						e.b = at
					}
					local[v] = e
				}
			}
			mu.Lock()
			ptrCnt += n
			for k, e := range local {
				g := refs[k]
				if g.a == 0 {
					g.a = e.a
				}
				if g.b == 0 {
					g.b = e.b
				}
				refs[k] = g
			}
			mu.Unlock()
		}()
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()

	// 2) 漏斗① +0xD8 合法坐标
	// 注意：目标地址在 guest 堆里稀疏分布（10 万目标散在数 GB），大窗口批量读会
	// 放大成 GB 级读取量（实测 4MB 窗口 → 7s）。改为定长 12 字节直读 + 复用缓冲，
	// 单次 RPM 约 5µs，10 万目标 ≈ 0.5s。
	targets := make([]uintptr, 0, len(refs))
	for t := range refs {
		targets = append(targets, t)
	}
	stage1 := make([]uintptr, 0, 64)
	var tb [12]byte
	for _, tgt := range targets {
		var got uintptr
		r, _, _ := procReadProcessMemory.Call(h, tgt+swPosFieldOff,
			uintptr(unsafe.Pointer(&tb[0])), 12, uintptr(unsafe.Pointer(&got)))
		if r == 0 || got != 12 {
			continue
		}
		x := math.Float32frombits(binary.LittleEndian.Uint32(tb[0:]))
		y := math.Float32frombits(binary.LittleEndian.Uint32(tb[4:]))
		z := math.Float32frombits(binary.LittleEndian.Uint32(tb[8:]))
		if swTripleStrict(x, y, z) {
			stage1 = append(stage1, tgt)
		}
	}

	// 3) 漏斗② +0x120 自指
	stage2 := make([]uintptr, 0, 8)
	for _, obj := range stage1 {
		if b := readMem(h, obj+swSelfRefOff, 8); len(b) == 8 {
			if uintptr(binary.LittleEndian.Uint64(b)) == obj+swSelfRefOff {
				stage2 = append(stage2, obj)
			}
		}
	}

	ms := time.Since(t0).Milliseconds()
	if len(stage2) != 1 {
		fmt.Printf("  [funnel] 结构漏斗未唯一命中（%d 指针 / %d 目标 → 坐标 %d → 自指 %d，%dms）——回退海选扫描 / Funnel not unique\n",
			ptrCnt, len(refs), len(stage1), len(stage2), ms)
		return swFunnelHit{}, false
	}
	obj := stage2[0]
	pos, ok := swReadTriple(h, obj+swPosFieldOff)
	if !ok {
		return swFunnelHit{}, false
	}
	e := refs[obj]
	fmt.Printf("  [funnel] 结构漏斗唯一命中 obj=0x%X 坐标=(%.1f, %.1f, %.1f) 槽=0x%X/0x%X（%d 指针 / %d 目标 → 坐标 %d → 自指 1，%dms） / Funnel resolved player object\n",
		obj, pos[0], pos[1], pos[2], e.a, e.b, ptrCnt, len(refs), len(stage1), ms)
	return swFunnelHit{obj: obj, slots: [2]uintptr{e.a, e.b}, pos: pos}, true
}

// ---------- 重建槽位学习缓存 ----------

var (
	swLearnMu    sync.Mutex
	swLearnH     uintptr
	swLearnSlots []swStaticChain
)

func swLearnedChains(h uintptr) []swStaticChain {
	swLearnMu.Lock()
	defer swLearnMu.Unlock()
	if swLearnH != h {
		return nil
	}
	return swLearnSlots
}

func swLearnChains(h uintptr, hit swFunnelHit) []swStaticChain {
	out := make([]swStaticChain, 0, 2)
	for _, s := range hit.slots {
		if s >= swModuleBase && s < swModuleBase+swModuleSize {
			out = append(out, swStaticChain{s, swPosFieldOff})
		}
	}
	swLearnMu.Lock()
	swLearnH, swLearnSlots = h, out
	swLearnMu.Unlock()
	return out
}

// ---------- 漏斗节流 ----------

var (
	swFunnelMu sync.Mutex
	swFunnelH  uintptr
	swFunnelAt time.Time
)

func swFunnelThrottled(h uintptr) bool {
	swFunnelMu.Lock()
	defer swFunnelMu.Unlock()
	if swFunnelH != h {
		swFunnelH, swFunnelAt = h, time.Time{}
	}
	if time.Since(swFunnelAt) < swFunnelEvery {
		return true
	}
	swFunnelAt = time.Now()
	return false
}

// swChainSet 本轮参与投票的链集合与主链索引。
// 学到槽位时把学到的那组放最前当主链（漏斗唯一命中的真身槽，比标定链更可信），
// 标定链继续参与佐证投票。
func swChainSet(h uintptr) ([]swStaticChain, int) {
	if learned := swLearnedChains(h); len(learned) > 0 {
		set := make([]swStaticChain, 0, len(learned)+len(savageWindStaticChains))
		set = append(set, learned...)
		set = append(set, savageWindStaticChains...)
		return set, 0
	}
	return savageWindStaticChains, savageWindChainPrimary
}

// swFunnelFallback L0 票数不足时的 L1：先用已学槽位直读，不行再现场跑漏斗。
// 漏斗判据比投票更硬（唯一命中 + 自指），命中即按 2 票返回。
func swFunnelFallback(h uintptr) ([3]float32, int, uintptr, bool) {
	if slots := swLearnedChains(h); len(slots) > 0 {
		ch := slots[0]
		if b := readMem(h, ch.static, 8); len(b) == 8 {
			if p := uintptr(binary.LittleEndian.Uint64(b)); p >= swModuleBase+swModuleSize && p < swGuestHi {
				if pos, ok := swReadTriple(h, p+ch.off); ok && WorldCoordOK(pos[0], pos[1], pos[2]) {
					return pos, 2, p + ch.off, true
				}
			}
		}
	}
	if swFunnelThrottled(h) {
		return [3]float32{}, 0, 0, false
	}
	hit, ok := resolveSavageWindFunnel(h)
	if !ok {
		return [3]float32{}, 0, 0, false
	}
	learned := swLearnChains(h, hit)
	addr := hit.obj + swPosFieldOff
	if len(learned) > 0 {
		addr = learned[0].static + learned[0].off
	}
	return hit.pos, 2, addr, true
}
