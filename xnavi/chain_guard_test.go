// chain_guard_test.go — 指针链护栏 G3 结构指纹的单元测试。
// G1（模块指纹）与 G2（指针域）依赖进程内存，走真机/CI 手工验证。

package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func putF32(b []byte, off int, v float32) {
	binary.LittleEndian.PutUint32(b[off:], math.Float32bits(v))
}

// 身份旋转矩阵（行列单位阵，正交归一，rotOKF 必过）
func identityRot() [9]float32 {
	return [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

func TestSwChainSampleOK_Valid(t *testing.T) {
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5) // 大世界合法坐标
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	rot := identityRot()
	for i, v := range rot {
		putF32(cb, 12+i*4, v)
	}
	pos, ok := swChainSampleOK(cb)
	if !ok {
		t.Fatalf("valid sample rejected")
	}
	if pos[0] != -1000.5 || pos[1] != 200.0 || pos[2] != 1800.0 {
		t.Fatalf("coords roundtrip broken: %v", pos)
	}
}

func TestSwChainSampleOK_BadCoord(t *testing.T) {
	cb := make([]byte, 48)
	putF32(cb, 0, 1e12) // WorldCoordOK 必拒的野值
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	rot := identityRot()
	for i, v := range rot {
		putF32(cb, 12+i*4, v)
	}
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("wild coordinate accepted")
	}
}

func TestSwChainSampleOK_BadMatrix(t *testing.T) {
	// 坐标合法但"旋转矩阵"缩放 2 倍（非正交归一）-> 必须拒绝
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5)
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	for i, v := range identityRot() {
		putF32(cb, 12+i*4, v*2)
	}
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("non-orthonormal matrix accepted")
	}
}

func TestSwChainSampleOK_NaNMatrix(t *testing.T) {
	// NaN 矩阵（对象内存被清/损坏的典型形态）-> 必须拒绝
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5)
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	rot := identityRot()
	for i, v := range rot {
		if i == 0 {
			v = float32(math.NaN())
		}
		putF32(cb, 12+i*4, v)
	}
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("NaN matrix accepted")
	}
}

func TestSwChainSampleOK_ShortBuffer(t *testing.T) {
	if _, ok := swChainSampleOK(make([]byte, 12)); ok {
		t.Fatalf("short buffer accepted")
	}
	if _, ok := swChainSampleOK(nil); ok {
		t.Fatalf("nil buffer accepted")
	}
}

func TestSwChainSampleOK_ZeroedSample(t *testing.T) {
	// 全零样本：坐标 (0,0,0) 与全零"矩阵"都应拒绝（槽位被清零 ≠ 玩家对象）
	cb := make([]byte, 48)
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("zeroed sample accepted")
	}
}
