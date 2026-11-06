// chain_guard_test.go — 指针链护栏 G3 结构指纹（宽松版）的单元测试。
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

func identityRot() [9]float32 {
	return [9]float32{1, 0, 0, 0, 1, 0, 0, 0, 1}
}

func validSample() []byte {
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5) // 大世界合法坐标
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	for i, v := range identityRot() {
		putF32(cb, 12+i*4, v)
	}
	return cb
}

func TestSwChainSampleOK_RotMatrix(t *testing.T) {
	pos, ok := swChainSampleOK(validSample())
	if !ok {
		t.Fatalf("valid sample rejected")
	}
	if pos[0] != -1000.5 || pos[1] != 200.0 || pos[2] != 1800.0 {
		t.Fatalf("coords roundtrip broken: %v", pos)
	}
}

func TestSwChainSampleOK_Quat(t *testing.T) {
	// 单位四元数 (0,0,0,1)：0x40 步长环缓冲对象（位置+四元数）的特征
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5)
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	putF32(cb, 24, 1.0)
	pos, ok := swChainSampleOK(cb)
	if !ok {
		t.Fatalf("quaternion sample rejected")
	}
	if pos[2] != 1800.0 {
		t.Fatalf("coords roundtrip broken: %v", pos)
	}
}

func TestSwChainSampleOK_UnknownLayoutLive(t *testing.T) {
	// 布局未知但数据活跃（非全零）-> 容错放行（交给多链投票兜底）
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5)
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	cb[12] = 0xAB
	if _, ok := swChainSampleOK(cb); !ok {
		t.Fatalf("unknown-but-live layout rejected (fault tolerance broken)")
	}
}

func TestSwChainSampleOK_ZeroedTrailing(t *testing.T) {
	// 坐标合法但后随 36 字节全零 = 死内存 -> 拒绝
	cb := make([]byte, 48)
	putF32(cb, 0, -1000.5)
	putF32(cb, 4, 200.0)
	putF32(cb, 8, 1800.0)
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("zeroed trailing accepted")
	}
}

func TestSwChainSampleOK_BadCoord(t *testing.T) {
	cb := validSample()
	putF32(cb, 0, 1e12) // WorldCoordOK 必拒的野值
	if _, ok := swChainSampleOK(cb); ok {
		t.Fatalf("wild coordinate accepted")
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
	// 全零样本：坐标 (0,0,0) 本身就不合法
	if _, ok := swChainSampleOK(make([]byte, 48)); ok {
		t.Fatalf("zeroed sample accepted")
	}
}
