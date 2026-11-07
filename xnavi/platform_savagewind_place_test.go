package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlaceLine(t *testing.T) {
	// 真机实测行（2026-11-07，与指针链读数逐位一致）
	pos, ok := swParsePlaceLine(`[place] swr:1 x=555.09 y=182.91 z=1905.53 yaw=140.7 pitch=-21.0 hour=5.98`)
	if !ok {
		t.Fatal("合法 [place] 行解析失败")
	}
	want := [3]float32{555.09, 182.91, 1905.53}
	for i := range want {
		if math.Abs(float64(pos[i]-want[i])) > 0.01 {
			t.Fatalf("第 %d 轴解析错误: got %.2f want %.2f", i, pos[i], want[i])
		}
	}

	bad := []string{
		`[place] swr:1 x=0 y=0 z=0 yaw=0 pitch=0 hour=1`, // 原点（WorldCoordOK 必拒）
		`[place] malformed`,
		`[place] swr:1 x=99999 y=0 z=0`, // 超范围
		`[info] something else`,
		``,
	}
	for _, ln := range bad {
		if _, ok := swParsePlaceLine(ln); ok {
			t.Fatalf("非法行被接受: %q", ln)
		}
	}
}

func TestReadLogTail(t *testing.T) {
	// 日志被截断到尾部窗口时，仍应能取到最后一行 [place]
	dir := t.TempDir()
	p := filepath.Join(dir, "savagewind.log")
	content := "[info] boot\n[place] swr:1 x=100.5 y=120.5 z=300.5 yaw=0 pitch=0 hour=1\n[info] tail noise\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	text, ok := swReadLogTail(p, 90)
	if !ok {
		t.Fatal("读日志尾部失败")
	}
	found := false
	for _, ln := range strings.Split(text, "\n") {
		if _, ok := swParsePlaceLine(strings.TrimSpace(ln)); ok {
			found = true
		}
	}
	if !found {
		t.Fatalf("尾部窗口内没找到 [place] 行: %q", text)
	}

	// 窗口小到切在行中间：必须丢弃残行而不是返回半截内容
	small, ok := swReadLogTail(p, 40)
	if ok {
		for _, ln := range strings.Split(small, "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "x=") {
				t.Fatalf("残行未被丢弃: %q", small)
			}
		}
	}
}
