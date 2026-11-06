// watch_ryujinx.go — Ryujinx 专属追踪管道（与 Eden 统一：动力学根地址锁定，Run >= 2 门禁，零重扫传送接管）。
//
// 架构设计（与 Eden 100% 对齐，做减法）：
// - 动力学根地址扫描锁定：在 guest 内存块（t.p.Blocks(256.0)）中扫描锚点周边 120 米窗口；
// - Run >= 2 真身门禁：彻底排除静态数据与外观 Transform 槽，锁定真实运动 RingBuffer；
// - 走动仲裁（Arbitration）：初始锁首个有效实体，玩家走动（位移 0.5~150m）瞬间无缝仲裁至最活跃的实时实体；
// - 锁定后死守 lockedAddr：过图黑屏平滑保持，绝不随意 drop lock；
// - 原生支持骑马、骑大师摩托车、盾滑：动力学控制器每帧持续输出真实坐标，无需任何补丁逻辑；
// - 跨区大传送（>300m）直接接管，神庙进出（isShrineScale）平滑锚定与秒级恢复；
// - 全量标准化日志：[attach], [lock], [track], [warp], [shrine], [hold], [lost]。

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type RyujinxTracker struct {
	p          Platform // 平台能力（Ryujinx / Savage Wind 共用同一条扫描管道）
	label      string   // 日志里的平台名
	trackID    string   // 管道标识（日志/诊断用）
	minRun     int      // Run 门禁门槛：Ryujinx=2（林克 RingBuffer 特征）；Savage Wind=0
	useChains  bool     // Savage Wind：启用静态指针链快路径（platform_savagewind.go）
	rescanCh   chan *RescanRequest
	stopCh     chan struct{}
}

// P 打印平台名可替换的日志：格式串里的 "Ryujinx" 会按当前管道平台名替换，
// 避免为 Savage Wind 复制整份管道（两平台共用同一套扫描/仲裁/传送接管逻辑）。
func (t *RyujinxTracker) P(format string, a ...any) {
	if t.label != "" && t.label != "Ryujinx" {
		format = strings.ReplaceAll(format, "Ryujinx", t.label)
	}
	fmt.Printf(format, a...)
}

func NewRyujinxTracker(p *ryujinxPlatform) *RyujinxTracker {
	return &RyujinxTracker{
		p:        p,
		label:    "Ryujinx",
		trackID:  "botw-ryujinx",
		minRun:   2,
		rescanCh: make(chan *RescanRequest, 4),
		stopCh:   make(chan struct{}),
	}
}

// NewSavageWindTracker Savage Wind 管道：与 Ryujinx 同一套动态扫描/走动仲裁/传送接管。
// 实测（0.4.1-alpha.4 / 1.9.0）：林克 Transform 仍是 **0x40 步长、位置 + 单位四元数** 的
// 33 条环缓冲（Run=33），与 Eden/Ryujinx 完全同构，所以 Run>=2 门禁照旧启用——
// 它能把 guest RAM 里大量"物件摆放记录"（位置 + 物件 ID，无四元数）挡在外面。
func NewSavageWindTracker(p Platform) *RyujinxTracker {
	return &RyujinxTracker{
		p:         p,
		label:     "Savage Wind",
		trackID:   "botw-savagewind",
		minRun:    2,
		useChains: true,
		rescanCh:  make(chan *RescanRequest, 4),
		stopCh:    make(chan struct{}),
	}
}

func (t *RyujinxTracker) Name() string {
	if t.trackID != "" {
		return t.trackID
	}
	return "botw-ryujinx"
}

func (t *RyujinxTracker) Stop() {
	select {
	case <-t.stopCh:
	default:
		close(t.stopCh)
	}
}

func (t *RyujinxTracker) Rescan(req *RescanRequest) {
	select {
	case t.rescanCh <- req:
	default:
	}
}

// anyWorldTicking 判断世界是否仍在更新：逐个采样候选记录，任一条在 120ms 内发生字节变化
// 即认为世界在跑（此时若锁定记录纹丝不动，就是锁到了静态副本）。
// 全部静止 = 玩家大概率开着菜单/背包/加载中，世界暂停，应保锁而不是掉锁。
func (t *RyujinxTracker) anyWorldTicking(h uintptr, cands []candSlot) bool {
	n := len(cands)
	if n == 0 {
		return false
	}
	if n > 6 {
		n = 6
	}
	first := make([][]byte, n)
	for i := 0; i < n; i++ {
		first[i] = readMem(h, cands[i].addr, 64)
	}
	time.Sleep(120 * time.Millisecond)
	for i := 0; i < n; i++ {
		a := first[i]
		if len(a) == 0 {
			continue
		}
		b := readMem(h, cands[i].addr, 64)
		if len(b) != len(a) {
			continue
		}
		for j := range a {
			if a[j] != b[j] {
				return true
			}
		}
	}
	return false
}

// preferLiveGroups 按"两次采样之间字节是否变化"把活对象排到最前面。
//
// 背景（Savage Wind 实测）：游戏把"上次存档的玩家位置"也映射在 guest RAM 里，
// 结构与真身 Transform 完全同构（0x40 步长、位置 + 单位四元数，Run 同样成立），
// 而且它紧贴存档锚点，"距锚点最近"这一条会让它抢到第一名 —— 于是红点被钉在
// 读档位置不动，走动也永远 verified=false。
// 区别只有一处：静态快照整段字节永远不变；真身每帧都在重写（实测 0.15s 内
// 静态快照 0/1024 字节变化，真身 400+/1024 字节变化）。
//
// 2026-11-04 修正（大师模式新档"全静止"）：候选排序是 Run 降序，静态快照的 Run
// 偏偏最大（实测 33），真身只有 2~6 —— 只采前 64 名时名额全被静态副本占满，
// 于是每轮都判"窗口内候选全部静止"。改为按 Run 分层采样（每层各取一份），
// 第一轮全静止时再用窄窗口对剩余候选复采一轮兜底。
//
// 返回重排后的候选、活跃候选数量、以及采样到的最大字节变化量（诊断用）。
func (t *RyujinxTracker) preferLiveGroups(h uintptr, groups []EdenHit, cx, cy, cz float64) ([]EdenHit, int, int) {
	const maxSample = 384    // 第一轮采样上限（按 Run 分层，避免单一层吃满）
	const perRun = 64        // 每个 Run 层的采样上限
	const fallbackMax = 1536 // 第二轮兜底采样上限
	const span = 0x400
	const fallbackSpan = 0x100
	const warm = 150 * time.Millisecond

	if len(groups) == 0 {
		return groups, 0, 0
	}

	off := func(a uintptr) uintptr {
		if a > 0x80 {
			return a - 0x80
		}
		return a
	}

	scores := make([]int, len(groups))    // 1KB 窗口内的字节变化数
	posScores := make([]int, len(groups)) // 坐标自身（12 字节）的变化数
	for i := range scores {
		scores[i] = -1 // -1 = 未采样
		posScores[i] = -1
	}

	// sample 对给定下标各读两次，返回（跳动组数, 最大字节变化）。
	sample := func(idxs []int, sp int) (int, int) {
		first := make([][]byte, len(idxs))
		for k, gi := range idxs {
			first[k] = readMem(h, off(groups[gi].Addr), sp)
		}
		time.Sleep(warm)
		live, bestCh := 0, 0
		for k, gi := range idxs {
			a := first[k]
			if len(a) == 0 {
				continue
			}
			b := readMem(h, off(groups[gi].Addr), sp)
			if len(b) != len(a) {
				continue
			}
			c := 0
			for j := range a {
				if a[j] != b[j] {
					c++
				}
			}
			// 坐标自身（窗口偏移 0x80 起的 12 字节）是否在变 —— 判「实体是否活着」的关键。
			// 只看窗口字节变化会误判：静态快照自己的坐标恒定，但周围 1KB 里有别的对象在动，
			// 照样能蹭到很高的分数。实测因此锁到过偏 536 米的副本（坐标 4 分钟零变化）。
			pc := 0
			const po = 0x80
			if len(a) >= po+12 {
				for j := po; j < po+12; j++ {
					if a[j] != b[j] {
						pc++
					}
				}
			}
			scores[gi] = c
			posScores[gi] = pc
			if pc >= 1 {
				live++
			}
			if c > bestCh {
				bestCh = c
			}
		}
		return live, bestCh
	}

	// 第一轮：按 Run 分层采样。不能只取前 N 名 —— 静态快照的 Run 往往最大
	// （实测 Run=33），真身 Run 只有 2~6，按 Run 降序会把采样名额全给静态副本。
	pick := livenessSample(groups, maxSample, perRun)
	liveN, best := sample(pick, span)

	// 第一轮全静止：真身可能排在采样名额之外，用窄窗口把剩余候选再采一遍。
	if liveN == 0 && len(groups) > len(pick) {
		sampled := make(map[int]bool, len(pick))
		for _, gi := range pick {
			sampled[gi] = true
		}
		rest := make([]int, 0, len(groups)-len(pick))
		for i := range groups {
			if !sampled[i] {
				rest = append(rest, i)
			}
		}
		if len(rest) > fallbackMax {
			rest = rest[:fallbackMax]
		}
		if l2, b2 := sample(rest, fallbackSpan); l2 > 0 {
			liveN += l2
			if b2 > best {
				best = b2
			}
		}
	}

	// 两轮都没跳动（可能整段都没加载好）：保持原排序，交给上层的走动仲裁处理。
	if liveN == 0 {
		t.P("  [verify] 候选共 %d 组，采样 %d 组坐标均未变化（静态数据）/ %d groups, no coordinate moved in 2 passes\n",
			len(groups), len(pick), len(groups))
		for i := 0; i < len(pick) && i < 6; i++ {
			g := groups[pick[i]]
			t.P("  [verify] 候选#%d 0x%X (%.1f, %.1f, %.1f) run=%d 坐标变化 %d 字节 / 窗口 %d 字节 / candidate liveness\n",
				i+1, g.Addr, g.P[0], g.P[1], g.P[2], g.Run, posScores[pick[i]], scores[pick[i]])
		}
		return groups, 0, 0
	}

	t.P("  [verify] 活跃度校验：采样 %d/%d 组，%d 组坐标在变化，已把活对象排到前面 / liveness: %d live of %d sampled\n",
		len(pick), len(groups), liveN, liveN, len(pick))

	// 排序：坐标自己在动的优先（这些才是活实体）。
	// 同类里按「距存档锚点」由近到远 —— 玩家是从存档点走出去的，通常是离锚点最近的活物；
	// 不能按变化字节排：动物 / 怪物 / 特效跳得比玩家猛，实测会把玩家挤到后面，
	// 锁到 138m 外的 NPC。
	dist := func(g EdenHit) float64 {
		dx := float64(g.P[0]) - cx
		dy := float64(g.P[1]) - cy
		dz := float64(g.P[2]) - cz
		return math.Sqrt(dx*dx + dy*dy + dz*dz)
	}
	liveIdx := make([]int, 0, len(groups))
	deadIdx := make([]int, 0, len(groups))
	for i, pc := range posScores {
		if pc >= 1 {
			liveIdx = append(liveIdx, i)
		} else {
			deadIdx = append(deadIdx, i)
		}
	}
	byDist := func(idx []int) {
		sort.SliceStable(idx, func(a, b int) bool { return dist(groups[idx[a]]) < dist(groups[idx[b]]) })
	}
	byDist(liveIdx)
	byDist(deadIdx)

	out := make([]EdenHit, 0, len(groups))
	for _, gi := range liveIdx {
		out = append(out, groups[gi])
	}
	for _, gi := range deadIdx {
		out = append(out, groups[gi])
	}
	return out, liveN, best
}

// livenessSample 选活跃度采样下标：按 Run 分层，每层最多 perRun 个，总量 maxSample。
// 不能让 Run 最大的静态快照把名额吃光（Savage Wind 实测：静态副本 Run=33，
// 真身 Run 只有 2~6，前 64 名清一色静态）。
func livenessSample(groups []EdenHit, maxSample, perRun int) []int {
	if len(groups) <= maxSample {
		out := make([]int, len(groups))
		for i := range out {
			out[i] = i
		}
		return out
	}
	per := map[int]int{}
	out := make([]int, 0, maxSample)
	for i, g := range groups {
		if per[g.Run] >= perRun {
			continue
		}
		per[g.Run]++
		out = append(out, i)
		if len(out) >= maxSample {
			break
		}
	}
	return out
}

// readRyujinxActor 直接按小端读取 12 字节并经 WorldCoordOK 校验。
func readRyujinxActor(h uintptr, addr uintptr) ([3]float32, bool) {
	buf := readMem(h, addr, 12)
	if len(buf) < 12 {
		return [3]float32{}, false
	}
	x := math.Float32frombits(binary.LittleEndian.Uint32(buf[0:]))
	y := math.Float32frombits(binary.LittleEndian.Uint32(buf[4:]))
	z := math.Float32frombits(binary.LittleEndian.Uint32(buf[8:]))
	if !WorldCoordOK(x, y, z) {
		return [3]float32{}, false
	}
	return [3]float32{x, y, z}, true
}

func (t *RyujinxTracker) Start(ctx context.Context) {
	t.P("  [pipeline] Ryujinx 追踪管道已启动 (动力学根运动缓冲 / Run>=%d 门禁) / Ryujinx pipeline started (dynamic root core: Run>=%d gate)\n", t.minRun, t.minRun)

	tick := time.NewTicker(100 * time.Millisecond) // 10Hz
	defer tick.Stop()

	var lockedAddr uintptr
	var cur, prev [3]float32
	hasPrev := false
	verified := false
	moveN := 0
	shrineMode := false
	var shrineEntrance [3]float32
	var cands []candSlot

	var manualTarget [3]float32
	hasManual := false
	lastScanAt := time.Time{}
	scanCooldown := time.Second
	holdLogged := false
	shrineAnchorWarned := false
	lockSince := time.Time{}
	lastUnverifiedHint := time.Time{}

	// 静态指针链模式（Savage Wind）：链有效期间完全绕过扫描/仲裁状态机
	chainMode := false
	chainHoldLogged := false

	reset := func(reason string) {
		lockedAddr = 0
		hasPrev = false
		verified = false
		moveN = 0
		shrineMode = false
		cands = nil
		holdLogged = false
		lockSince = time.Time{}
		lastUnverifiedHint = time.Time{}
		chainMode = false
		chainHoldLogged = false
		goUnlocked(reason)
	}

	// lastScanStatic 记录上一次扫描"窗口内所有候选都静止"（世界未更新 / 窗口偏离真身）。
	// 这种情况绝不能锁定：锁到的必然是静态副本，之后怎么走都不会跟随。
	var lastScanStatic bool
	var lastStaticHint time.Time

	scanNear := func(center [3]float32, radius float32) bool {
		lastScanStatic = false
		h := t.p.Handle()
		blks := t.p.Blocks(256.0)
		if len(blks) == 0 {
			return false
		}

		var allHits []EdenHit
		var mu sync.Mutex

		const chunk = 8 << 20
		const overlap = 256
		const sleepPerChunk = 4 * time.Millisecond
		workers := 4

		type scanJob struct {
			addr uintptr
			n    int
		}
		var jobs []scanJob
		for _, b := range blks {
			if b.Size == 0 {
				continue
			}
			for off := uintptr(0); off < b.Size; off += uintptr(chunk) {
				n := chunk
				if remain := int(b.Size - off); remain < n {
					n = remain
				}
				if uintptr(n) < b.Size-off {
					n += overlap
				}
				jobs = append(jobs, scanJob{b.Base + off, n})
			}
		}
		if len(jobs) == 0 {
			return false
		}

		ch := make(chan scanJob, workers*2)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := range ch {
					buf := readMem(h, j.addr, j.n)
					time.Sleep(sleepPerChunk)
					if len(buf) < 12 {
						continue
					}
					hits := FindEdenTriples(buf, j.addr, &center, radius)
					if len(hits) == 0 {
						continue
					}
					mu.Lock()
					allHits = append(allHits, hits...)
					mu.Unlock()
				}
			}()
		}
		for _, j := range jobs {
			ch <- j
		}
		close(ch)
		wg.Wait()

		// 门禁①：坐标通过 WorldCoordOK
		// 门禁②：硬门槛 Run >= 2（真林克专属 RingBuffer 特征，彻底排除静态数据）
		var identHits []EdenHit
		for _, hit := range allHits {
			if !WorldCoordOK(hit.P[0], hit.P[1], hit.P[2]) {
				continue
			}
			if hit.Run >= t.minRun {
				identHits = append(identHits, hit)
			}
		}
		if len(identHits) == 0 {
			return false
		}

		groups := GroupEdenHits(identHits)
		if len(groups) == 0 {
			return false
		}

		// 门禁③：活对象优先。存档里保存的玩家位置在 guest RAM 中往往还有一份静态快照
		// （Run 同样成立，且与本存档锚点几乎重合，因此会靠"距锚点最近"抢到第一名），
		// 但它的字节永不变化；真身 Transform 每帧都在重写。
		groups, liveN, liveBytes := t.preferLiveGroups(h, groups, float64(center[0]), float64(center[1]), float64(center[2]))

		// 门禁④：窗口内一个候选都不跳 -> 绝不锁定。
		// 静止意味着世界没在更新（菜单/加载），或存档锚点太旧、真身早已走出窗口，
		// 此时窗口里只剩静态快照，锁上去必然坐标冻结。返回 false 让外层放大窗口重扫。
		if liveN == 0 {
			lastScanStatic = true
			return false
		}

		best := groups[0]
		lockedAddr = best.Addr
		cur = best.P
		prev = cur
		hasPrev = true
		verified = false
		moveN = 0
		shrineMode = false
		holdLogged = false
		lockSince = time.Now()
		lastUnverifiedHint = time.Time{}

		var slots []candSlot
		for i := 0; i < len(groups) && i < 5; i++ {
			slots = append(slots, candSlot{addr: groups[i].Addr, init: groups[i].P})
		}
		cands = slots

		setLock(lockedAddr, false, len(groups), "ryujinx-scan")
		publishState(true, cur[0], cur[1], cur[2], false, len(groups), "ryujinx-scan")
		t.P("  [verify] Ryujinx 捕获动力学候选地址 Addr: 0x%X (%.1f, %.1f, %.1f) -> 【请在游戏内移动角色以完成验证】 / Candidate locked: 0x%X -> [Please move Link in-game to verify]\n",
			lockedAddr, cur[0], cur[1], cur[2], lockedAddr)
		if liveN > 0 {
			t.P("  [verify] Ryujinx 活跃度校验：%d 个候选在跳动，选中地址 0.15s 内变化 %d 字节（静态快照会被排到后面） / Liveness check: %d candidates ticking, locked one changed %d bytes/0.15s\n",
				liveN, liveBytes, liveN, liveBytes)
		}
		return true
	}

	for {
		select {
		case <-ctx.Done():
			t.P("  [pipeline] Ryujinx 追踪管道已停止 (上下文取消) / Ryujinx pipeline stopped (context cancelled)\n")
			return
		case <-t.stopCh:
			t.P("  [pipeline] Ryujinx 追踪管道已停止 (请求停止) / Ryujinx pipeline stopped (stop requested)\n")
			return
		case req := <-t.rescanCh:
			reset("Ryujinx /rescan requested")
			if req != nil && req.HasTarget {
				hasManual = true
				manualTarget = [3]float32{req.TargetX, 0, req.TargetZ}
				t.P("  [scan] Ryujinx 手工校准目标: (%.1f, %.1f) / Ryujinx manual target set: (%.1f, %.1f)\n", req.TargetX, req.TargetZ, req.TargetX, req.TargetZ)
			}
		case <-tick.C:
			// 1. 确保模拟器附加
			if !t.p.Attach() {
				if lockedAddr != 0 {
					t.P("  [lost] Ryujinx 模拟器进程断开或退出 / Ryujinx emulator process exited or detached\n")
					reset("Ryujinx lost")
				} else {
					state.mu.Lock()
					state.ok = false
					state.source = "waiting for Ryujinx"
					state.mu.Unlock()
				}
				continue
			}

			h := t.p.Handle()

			// 1.5 Savage Wind 静态指针链（快路径）：游戏原生寻址，免扫描免仲裁免走动确认。
			// 链条由游戏自己维护（死亡/传送/过图自动更新指针），只在链整体失效时
			// （加载过渡）保持坐标等待自愈；从未生效（布局不匹配）则落回下方启发式。
			if t.useChains {
				pos, votes, caddr := readSavageWindChains(h)
				if votes >= 2 {
					if !chainMode {
						chainMode = true
						t.P("  [chain] Savage Wind 静态指针链接管（%d/%d 条链一致 @0x%X）——游戏原生寻址，免扫描免仲裁，死亡/传送/过图自动跟随 / Static pointer chains engaged (%d chains agree @0x%X)\n",
							votes, len(savageWindStaticChains), caddr, votes, caddr)
						setLock(caddr, true, votes, "sw-chain")
					}
					chainHoldLogged = false
					cur = pos

					// 神庙本地尺度处理（与扫描路径同规则：庙内钉入口）
					isShrine := isShrineScale(cur)
					if shrineMode {
						if !isShrine {
							shrineMode = false
							destName := formatDestName(cur)
							t.P("  [shrine] 角色离开试炼场 -> 恢复大世界追踪 (%.1f, %.1f, %.1f) [%s] / Left shrine -> Resumed world tracking\n",
								cur[0], cur[1], cur[2], destName)
						} else {
							publishState(true, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], true, votes, "shrine")
							continue
						}
					} else if isShrine && hasPrev && !isShrineScale(prev) && dist3(cur, prev) > 100.0 {
						shrineMode = true
						shrineEntrance = prev
						destName := formatDestName(prev)
						t.P("  [shrine] 角色进入试炼场 [%s] -> 锚定神庙入口 (%.1f, %.1f, %.1f) / Entered shrine [%s] -> Holding entrance\n",
							destName, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], destName)
						publishState(true, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], true, votes, "shrine")
						prev = cur
						continue
					}

					if hasPrev {
						delta := dist3(cur, prev)
						if delta > 300.0 {
							destName := formatDestName(cur)
							t.P("  [warp] Savage Wind 角色大传送 (距离=%.1fm) -> %s (%.1f, %.1f, %.1f) [指针链零损耗接管] / Fast travel (dist=%.1fm) -> %s [Chain takeover]\n",
								delta, destName, cur[0], cur[1], cur[2], delta, destName)
						} else if delta > 150.0 {
							// 单帧异常跳变（非传送）：保持，等下一 tick 链条自愈
							publishState(true, prev[0], prev[1], prev[2], true, votes, "hold")
							continue
						}
					}
					publishState(true, cur[0], cur[1], cur[2], true, votes, "sw-chain")
					prev = cur
					hasPrev = true
					continue
				}
				// 链整体失效：曾工作过 -> 加载/场景切换过渡，保持坐标等待自愈；
				// 从未工作（不同游戏版本/recomp 布局）-> 落回原有动态扫描管道。
				if chainMode && hasPrev {
					if !chainHoldLogged {
						t.P("  [chain] 静态链暂时全部失效（加载/场景切换）-> 保持坐标等待恢复 / Chains transiently invalid, holding position\n")
						chainHoldLogged = true
					}
					publishState(true, prev[0], prev[1], prev[2], true, 0, "chain-hold")
					continue
				}
			}

			// 2. UNLOCKED：guest 内存动态扫描（带 Run>=2 门禁）
			if lockedAddr == 0 {
				if time.Since(lastScanAt) < scanCooldown {
					continue
				}
				lastScanAt = time.Now()

				var scanCenter [3]float32
				var radius float32 = 120.0

				anchors := currentGame.SaveAnchors(t.p)
				var latestSaveAnchor *SaveAnchor
				saveIsFresh := false
				saveAge := time.Duration(0)
				if len(anchors) > 0 {
					latestSaveAnchor = anchors[0]
					if fi, err := os.Stat(latestSaveAnchor.Path); err == nil {
						saveAge = time.Since(fi.ModTime())
						if saveAge <= 120*time.Second {
							saveIsFresh = true
						}
					}
				}

				switch {
				case hasManual:
					scanCenter = manualTarget
					radius = 150.0
					t.P("  [scan] Ryujinx 扫描手工定向窗口 150m (%.1f, %.1f)... / Scanning manual window 150m near (%.1f, %.1f)...\n", scanCenter[0], scanCenter[2], scanCenter[0], scanCenter[2])
				case latestSaveAnchor != nil:
					// 始终以最新存档锚点为中心扫描（绝不拿切场景掉锁前的旧废弃坐标当中心）
					scanCenter = latestSaveAnchor.Pos
					radius = 120.0
					if saveIsFresh {
						t.P("  [scan] Ryujinx 发现新鲜存档 (<=120s)，扫描 120m 锚点窗口 (%.1f, %.1f, %.1f)... / Fresh save detected, scanning window 120m...\n",
							scanCenter[0], scanCenter[1], scanCenter[2])
					} else {
						// 存档越旧，玩家离锚点越远：直接放宽到 400m，
						// 免得先在 120m 里锁到一堆上一次的静态残留（锁上去就是坐标冻结）。
						if saveAge > 5*time.Minute {
							radius = 400.0
							t.P("  [scan] 存档已 %.0f 分钟未更新，放宽到 400m 窗口 (%.1f, %.1f, %.1f)... / Stale save (%.0fm), scanning wider 400m window...\n",
								saveAge.Minutes(), scanCenter[0], scanCenter[1], scanCenter[2], saveAge.Minutes())
						} else {
							t.P("  [scan] Ryujinx 扫描存档锚点 120m 窗口 (%.1f, %.1f, %.1f)... / Scanning save anchor window 120m...\n",
								scanCenter[0], scanCenter[1], scanCenter[2])
						}
					}
				default:
					state.mu.Lock()
					state.ok = false
					state.source = "waiting for save anchor (press [+] in game)"
					state.mu.Unlock()
					continue
				}

				found := scanNear(scanCenter, radius)
				if !found && !hasManual {
					found = scanNear(scanCenter, 400.0)
				}
				if !found && !hasManual {
					found = scanNear(scanCenter, 3000.0)
				}
				if found {
					hasManual = false
					scanCooldown = time.Second
				} else if lastScanStatic {
					// 全部窗口都是静止数据：世界此刻没有更新（菜单/加载/暂停），
					// 或存档锚点太旧且真身已走远。不锁定，退避后重试。
					scanCooldown = 6 * time.Second
					if time.Since(lastStaticHint) > 10*time.Second {
						t.P("  [scan] 窗口内候选全部静止 -> 暂不锁定（世界未更新或存档锚点过旧），6 秒后重试 / All candidates static, skip lock and retry in 6s\n")
						lastStaticHint = time.Now()
					}
					state.mu.Lock()
					state.source = "waiting for live world"
					state.mu.Unlock()
				} else {
					scanCooldown = time.Second
					t.P("  [scan] Ryujinx 扫描未发现活动玩家 (等待进入 3D 游戏世界)... / No live player found (waiting for 3D world)...\n")
				}
				continue
			}

			// 3. LOCKED：死守 lockedAddr，过图黑屏平滑保持，落地直接接管，绝不主动掉锁
			// 例外：未获走动确认、且锁定记录已完全停止写入（static）时，说明锁到的是静态副本，
			// 必须掉锁重扫，否则红点会永久冻结。
			// 但"全静止"也可能是玩家开着菜单/背包（世界暂停）——那时其它候选同样不动，
			// 这种情况要保住锁，否则菜单期间红点会凭空消失。
			if !verified && !lockSince.IsZero() && time.Since(lockSince) > 45*time.Second {
				frozen := func() bool {
					a := readMem(h, lockedAddr-0x80, 256)
					time.Sleep(120 * time.Millisecond)
					b := readMem(h, lockedAddr-0x80, 256)
					if len(a) != 256 || len(b) != 256 {
						return false
					}
					for i := range a {
						if a[i] != b[i] {
							return false
						}
					}
					return true
				}()
				if frozen && t.anyWorldTicking(h, cands) {
					t.P("  [relock] 锁定记录 45 秒无走动且不再写入，但其它候选仍在刷新 -> 判定为静态副本，重新扫描 / Stale lock (frozen while world live), rescanning\n")
					reset("relock-frozen")
					continue
				}
				lockSince = time.Now()
			}

			d, ok := readRyujinxActor(h, lockedAddr)
			if !ok {
				for i := 0; i < 3; i++ {
					time.Sleep(30 * time.Millisecond)
					if d, ok = readRyujinxActor(h, lockedAddr); ok {
						break
					}
				}
			}

			// 读不到标准三元组（过图黑屏 / 场景切换过渡期 / 瞬态读空）
			if !ok {
				if shrineMode {
					// 神庙内黑屏或瞬态：死死钉在神庙入口
					publishState(true, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], true, 0, "shrine")
					continue
				}

				if !holdLogged {
					t.P("  [hold] Ryujinx reading transient/loading -> holding position\n")
					holdLogged = true
				}
				if hasPrev {
					publishState(true, prev[0], prev[1], prev[2], verified, len(cands), "hold")
				}
				continue
			}

			// 读出合法世界坐标
			holdLogged = false
			cur = d
			isShrine := isShrineScale(cur)

			// 神庙内部试炼场本地尺度挂起
			if shrineMode {
				if !isShrine {
					// 出庙：脱离神庙本地尺度，恢复大世界跟踪
					shrineMode = false
					destName := formatDestName(cur)
					t.P("  [shrine] Ryujinx 角色离开试炼场 -> 恢复大世界追踪 (%.1f, %.1f, %.1f) [%s] / Left shrine -> Resumed world tracking\n",
						cur[0], cur[1], cur[2], destName)
					publishState(true, cur[0], cur[1], cur[2], verified, len(cands), "player")
					prev = cur
					hasPrev = true
					continue
				}
				// 还在神庙内：死锁保护，红点死死钉在神庙入口！
				publishState(true, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], true, 0, "shrine")
				continue
		} else if isShrine && dist3(cur, prev) > 100.0 {
			if !hasPrev || isShrineScale(prev) {
				// 拿不到合法入口锚点（启动/重扫时人已在庙内）：prev 可能还是零值 (0,0,0)，
				// 绝不能把它当门口，否则红点钉死在地图原点的海里。暂停发布，出庙后自愈。
				if !shrineAnchorWarned {
					shrineAnchorWarned = true
					t.P("  [shrine] Ryujinx 检测到神庙本地坐标但无合法入口锚点 -> 暂停发布（不钉原点） / Shrine-local coords without entrance anchor -> pausing publish (no origin pin)\n")
				}
				continue
			}
			// 进庙：世界尺度 → 神庙本地尺度，红点钉在进入前的门口 prev
			shrineMode = true
			shrineEntrance = prev
			destName := formatDestName(prev)
			t.P("  [shrine] Ryujinx 角色进入试炼场 [%s] -> 锚定神庙入口 (%.1f, %.1f, %.1f) / Entered shrine [%s] -> Holding entrance\n",
				destName, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], destName)
			publishState(true, shrineEntrance[0], shrineEntrance[1], shrineEntrance[2], true, 0, "shrine")
			continue
		}

			// 多候选走动仲裁（防驿站等多 NPC 场景误锁静止 NPC）
			if !verified && len(cands) > 1 {
				for _, c := range cands {
					if c.addr == lockedAddr {
						continue
					}
					if cd, okCd := readRyujinxActor(h, c.addr); okCd {
						dCand := dist3(cd, c.init)
						if dCand >= 0.5 && dCand < 150.0 {
							lockedAddr = c.addr
							cur = cd
							verified = true
							cands = nil
							setLock(lockedAddr, true, 1, "ryujinx-move-confirm")
							t.P("  [lock] Ryujinx 走动仲裁成功！真身锁定 Addr: 0x%X (%.1f, %.1f, %.1f) -> 【已完全锁定】 / Movement arbitration confirmed! Locked at 0x%X -> [Fully locked]\n",
								lockedAddr, cur[0], cur[1], cur[2], lockedAddr)
							break
						}
					}
				}
			}

			delta := dist3(cur, prev)

			// 传送判定（>300m）
			// ★ enavi 实测真理：传送后 addr 不变，坐标直接更新！绝不重扫换 addr！
			if hasPrev && delta > 300.0 {
				shrineMode = false
				cands = nil
				destName := formatDestName(cur)
				t.P("  [warp] Ryujinx 角色大传送 (距离=%.1fm) -> %s (%.1f, %.1f, %.1f) [零重扫接管] / Fast travel (dist=%.1fm) -> %s [Direct takeover]\n",
					delta, destName, cur[0], cur[1], cur[2], delta, destName)
				publishState(true, cur[0], cur[1], cur[2], verified, 1, "player")
				prev = cur
				continue
			}

			// 单帧位移异常过大（>150m 非传送跳变）
			if hasPrev && delta > 150.0 {
				publishState(true, prev[0], prev[1], prev[2], verified, len(cands), "hold")
				continue
			}

			// 正常跟随与移动确认
			if hasPrev && delta >= 0.5 {
				moveN++
				if moveN >= 3 && !verified {
					verified = true
					cands = nil
					setLock(lockedAddr, true, 1, "ryujinx-live")
					t.P("  [lock] Ryujinx 角色移动确认成功！真身锁定 Addr: 0x%X -> 【已完全锁定】 / Movement confirmed! Live actor locked at 0x%X -> [Fully locked]\n", lockedAddr, lockedAddr)
				}
			} else {
				moveN = 0
			}

			// 提示机制：若锁定后持续未确认走动，友好提醒用户
			if !verified && time.Since(lockSince) > 6*time.Second && time.Since(lastUnverifiedHint) > 15*time.Second {
				lastUnverifiedHint = time.Now()
				t.P("  [hint] 主角未确认走动 (verified=false)。请在游戏内走动几步 / Link movement not confirmed. Please move a few steps in-game\n")
			}

			publishState(true, cur[0], cur[1], cur[2], verified, len(cands), "player")
			prev = cur
			hasPrev = true
		}
	}
}
