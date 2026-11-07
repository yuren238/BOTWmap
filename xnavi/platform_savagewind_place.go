package main

// platform_savagewind_place.go — 从 savagewind.log 的 [place] 行取真值坐标
//
// 游戏在每次场景切换（读档 / 传送 / 进出神庙）时打印一行真值：
//
//	[place] swr:1 x=555.09 y=182.91 z=1905.53 yaw=140.7 pitch=-21.0 hour=5.98
//
// 实测（2026-11-07）：该行坐标与 18 条指针链读数逐位一致（差 0.00m）。
// 相比存档锚点（可能几分钟前的），它正好在"需要重锚"的时刻刷新，因此用作
// 海选扫描的中心锚点，可把窗口从 120~400m 收到 60m，锁定更快、更不容易锁到旧残留。
//
// 新鲜度判定：比对行内容与上次所见——内容变化 = 游戏刚打印 = 场景刚切换。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const swPlaceLogTail = 128 << 10 // 只读日志尾部 128KB

var (
	swPlaceMu   sync.Mutex
	swPlaceLine string
	swPlacePos  [3]float32
	swPlaceSeen time.Time
	swPlaceInit bool // 是否已读过一次（首次读到不算"新鲜"，避免启动时误用小窗口）
)

// swParsePlaceLine 解析 "[place] swr:1 x=... y=... z=..."。
func swParsePlaceLine(ln string) ([3]float32, bool) {
	if !strings.HasPrefix(ln, "[place]") {
		return [3]float32{}, false
	}
	var x, y, z float64
	if _, err := fmt.Sscanf(ln[7:], " swr:1 x=%f y=%f z=%f", &x, &y, &z); err != nil {
		return [3]float32{}, false
	}
	if !WorldCoordOK(float32(x), float32(y), float32(z)) {
		return [3]float32{}, false
	}
	return [3]float32{float32(x), float32(y), float32(z)}, true
}

func swReadLogTail(p string, n int64) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false
	}
	start := int64(0)
	truncated := false
	if fi.Size() > n {
		start = fi.Size() - n
		truncated = true
	}
	if _, err := f.Seek(start, 0); err != nil {
		return "", false
	}
	buf := make([]byte, fi.Size()-start)
	m, _ := f.Read(buf)
	if m <= 0 {
		return "", false
	}
	text := string(buf[:m])
	// 从文件中间开始读时，首行是残行（没有行首 "[place]"），丢掉它
	if truncated {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			return "", false
		}
	}
	return text, true
}

// savageWindPlaceAnchor 取日志尾部最后一个 [place] 真值坐标。
//   fresh=true 表示这一行是本次新出现的（≈刚刚读档/传送/进出神庙）
//   ok=false   表示日志不存在、太久没写，或没有任何合法 [place] 行
func savageWindPlaceAnchor(h uintptr) (pos [3]float32, fresh, ok bool) {
	exe := processExePath(h)
	if exe == "" {
		return
	}
	p := filepath.Join(filepath.Dir(exe), "savagewind.log")
	fi, err := os.Stat(p)
	if err != nil {
		return
	}
	// 日志 5 分钟没写 = 游戏侧没有活动，锚点不可信
	if time.Since(fi.ModTime()) > 5*time.Minute {
		return
	}
	text, rok := swReadLogTail(p, swPlaceLogTail)
	if !rok {
		return
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		ln := strings.TrimSpace(lines[i])
		pv, pok := swParsePlaceLine(ln)
		if !pok {
			continue
		}
		swPlaceMu.Lock()
		fresh = swPlaceInit && ln != swPlaceLine
		swPlaceInit = true
		if ln != swPlaceLine {
			swPlaceLine, swPlacePos, swPlaceSeen = ln, pv, time.Now()
		}
		pos = swPlacePos
		swPlaceMu.Unlock()
		return pos, fresh, true
	}
	return
}
