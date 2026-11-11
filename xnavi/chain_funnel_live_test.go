package main

// chain_funnel_live_test.go — 结构漏斗的真机验证（需要 savagewind.exe 在跑；CI 上自动跳过）
//
//	go test -run TestFunnelLive -v

import (
	"testing"
)

func TestFunnelLive(t *testing.T) {
	pid := findSavageWindPid()
	if pid == 0 {
		t.Skip("savagewind.exe 未运行，跳过真机验证")
	}
	h, err := openProcess(pid)
	if err != nil || h == 0 {
		t.Skipf("OpenProcess(%d) 失败（需要管理员权限）: %v，跳过", pid, err)
	}
	defer closeHandle(h)
	if !savageWindModuleOK(h) {
		t.Skip("guest 堆尚未就位（游戏还在标题画面），跳过真机验证")
	}

	hit, ok := resolveSavageWindFunnel(h)
	if !ok {
		t.Fatalf("漏斗未唯一命中（判据可能不符当前版本）")
	}
	if !WorldCoordOK(hit.pos[0], hit.pos[1], hit.pos[2]) {
		t.Fatalf("命中坐标非法: %v", hit.pos)
	}
	if hit.obj < swModuleBase+swModuleSize || hit.obj >= swGuestHi {
		t.Fatalf("命中对象不在 guest 堆: 0x%X", hit.obj)
	}
	t.Logf("命中 obj=0x%X 坐标=(%.1f, %.1f, %.1f) 槽=0x%X/0x%X",
		hit.obj, hit.pos[0], hit.pos[1], hit.pos[2], hit.slots[0], hit.slots[1])
	if hit.slots[0] != 0 && (hit.slots[0] < swModuleBase || hit.slots[0] >= swModuleBase+swModuleSize) {
		t.Fatalf("槽位不在静态区: 0x%X", hit.slots[0])
	}

	// 与 18 条标定链投票结果对比：都应指向同一玩家（误差 < 8m）
	pos2, votes, _ := readSavageWindChains(h)
	if votes >= 2 {
		d := dist3(pos2, hit.pos)
		if d > 8.0 {
			t.Fatalf("漏斗与链条结果不一致: 漏斗 %v / 链条 %v (差 %.1fm)", hit.pos, pos2, d)
		}
		t.Logf("与标定链一致（%d 票，差 %.2fm）", votes, d)
	}

	// 学到槽位后，L0 直读应能命中（模拟链失效后的自愈路径）
	learned := swLearnChains(h, hit)
	if len(learned) == 0 {
		t.Fatalf("槽位学习失败")
	}
	pos3, votes3, _ := readSavageWindChains(h)
	if votes3 < 2 {
		t.Fatalf("学到槽位后 L0 仍未接管（votes=%d）", votes3)
	}
	if d := dist3(pos3, hit.pos); d > 8.0 {
		t.Fatalf("学到槽位后坐标偏离 %.1fm", d)
	}
	t.Logf("学到 %d 条槽位后 L0 直读恢复：%d 票，%v", len(learned), votes3, pos3)
}

// TestProcessAliveLive 验证 processAlive 对运行中进程返回真（Attach 快路径的前提）。
func TestProcessAliveLive(t *testing.T) {
	pid := findSavageWindPid()
	if pid == 0 {
		t.Skip("savagewind.exe 未运行，跳过")
	}
	h, err := openProcess(pid)
	if err != nil || h == 0 {
		t.Skipf("OpenProcess(%d) 失败（需要管理员权限）: %v，跳过", pid, err)
	}
	defer closeHandle(h)
	if !processAlive(h) {
		t.Fatalf("运行中的进程 %d 被判为已退出", pid)
	}
	if processAlive(0) {
		t.Fatal("空句柄被判为存活")
	}
}

// TestPlaceAnchorLive 验证 savagewind.log 的 [place] 锚点与链读数一致。
// 需要 savagewind.exe 在跑；CI 上自动跳过。
func TestPlaceAnchorLive(t *testing.T) {
	pid := findSavageWindPid()
	if pid == 0 {
		t.Skip("savagewind.exe 未运行，跳过真机验证")
	}
	h, err := openProcess(pid)
	if err != nil || h == 0 {
		t.Skipf("OpenProcess(%d) 失败（需要管理员权限）: %v，跳过", pid, err)
	}
	defer closeHandle(h)
	if !savageWindModuleOK(h) {
		t.Skip("guest 堆尚未就位（游戏还在标题画面），跳过真机验证")
	}

	pos, fresh, ok := savageWindPlaceAnchor(h)
	if !ok {
		t.Skip("savagewind.log 里没有可用的 [place] 行")
	}
	t.Logf("[place] 锚点 (%.2f, %.2f, %.2f) fresh=%v", pos[0], pos[1], pos[2], fresh)

	pos2, votes, _ := readSavageWindChains(h)
	if votes < 2 {
		t.Logf("链条当前不可用（%d 票），跳过一致性比对", votes)
		return
	}
	// 场景切换后玩家可能已走动，只做宽松校验：差距应远小于旧存档漂移（>300m）
	if d := dist3(pos, pos2); d > 300.0 {
		t.Fatalf("[place] 锚点与链条读数相差 %.0fm，锚点可能来自其它存档", d)
	} else {
		t.Logf("与链条读数差 %.1fm（%d 票）", d, votes)
	}
}
