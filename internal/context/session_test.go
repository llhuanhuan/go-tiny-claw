package context

import (
	"sync"
	"testing"
)

// ============================================================
// 场景 1: 基础计费累加
// ============================================================

// TestSession_RecordUsage_BasicAccumulation 验证单次调用后计费数据正确累加。
func TestSession_RecordUsage_BasicAccumulation(t *testing.T) {
	s := NewSession("test-basic")

	s.RecordUsage(1000, 500, 0.00075)

	if s.TotalPromptTokens != 1000 {
		t.Fatalf("期望 PromptTokens=1000, 得到 %d", s.TotalPromptTokens)
	}
	if s.TotalCompletionTokens != 500 {
		t.Fatalf("期望 CompletionTokens=500, 得到 %d", s.TotalCompletionTokens)
	}
	if s.TotalCostCNY != 0.00075 {
		t.Fatalf("期望 CostCNY=0.00075, 得到 %f", s.TotalCostCNY)
	}
}

// ============================================================
// 场景 2: 多次累加
// ============================================================

// TestSession_RecordUsage_MultipleCalls 验证多次 RecordUsage 正确累加。
func TestSession_RecordUsage_MultipleCalls(t *testing.T) {
	s := NewSession("test-multi")

	s.RecordUsage(1000, 500, 0.001)
	s.RecordUsage(2000, 800, 0.002)
	s.RecordUsage(500, 200, 0.0005)

	if s.TotalPromptTokens != 3500 {
		t.Fatalf("期望 PromptTokens=3500, 得到 %d", s.TotalPromptTokens)
	}
	if s.TotalCompletionTokens != 1500 {
		t.Fatalf("期望 CompletionTokens=1500, 得到 %d", s.TotalCompletionTokens)
	}
	if s.TotalCostCNY != 0.0035 {
		t.Fatalf("期望 CostCNY=0.0035, 得到 %f", s.TotalCostCNY)
	}
}

// ============================================================
// 场景 3: 零值输入
// ============================================================

// TestSession_RecordUsage_ZeroTokens 验证零值输入不会导致异常。
func TestSession_RecordUsage_ZeroTokens(t *testing.T) {
	s := NewSession("test-zero")

	s.RecordUsage(0, 0, 0.0)

	if s.TotalPromptTokens != 0 {
		t.Fatalf("期望 PromptTokens=0, 得到 %d", s.TotalPromptTokens)
	}
	if s.TotalCostCNY != 0.0 {
		t.Fatalf("期望 CostCNY=0.0, 得到 %f", s.TotalCostCNY)
	}
}

// ============================================================
// 场景 4: 并发安全 —— 多 goroutine 同时 RecordUsage
// ============================================================

// TestSession_RecordUsage_ConcurrentSafety 用 go test -race 验证无数据竞争。
func TestSession_RecordUsage_ConcurrentSafety(t *testing.T) {
	s := NewSession("test-concurrent")
	const goroutines = 100
	const callsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				s.RecordUsage(10, 5, 0.00001)
			}
		}()
	}

	wg.Wait()

	expectedPrompt := goroutines * callsPerGoroutine * 10
	expectedCompletion := goroutines * callsPerGoroutine * 5
	expectedCost := float64(goroutines*callsPerGoroutine) * 0.00001

	if s.TotalPromptTokens != expectedPrompt {
		t.Fatalf("期望 PromptTokens=%d, 得到 %d", expectedPrompt, s.TotalPromptTokens)
	}
	if s.TotalCompletionTokens != expectedCompletion {
		t.Fatalf("期望 CompletionTokens=%d, 得到 %d", expectedCompletion, s.TotalCompletionTokens)
	}
	// 浮点精度容差
	diff := s.TotalCostCNY - expectedCost
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Fatalf("期望 CostCNY≈%f, 得到 %f", expectedCost, s.TotalCostCNY)
	}
}

// ============================================================
// 场景 5: NewSession 初始化验证
// ============================================================

// TestSession_NewSession_InitialValues 验证新建 Session 的初始状态。
func TestSession_NewSession_InitialValues(t *testing.T) {
	s := NewSession("init-test")

	if s.ID != "init-test" {
		t.Fatalf("期望 ID='init-test', 得到 %q", s.ID)
	}
	if s.TotalPromptTokens != 0 || s.TotalCompletionTokens != 0 || s.TotalCostCNY != 0 {
		t.Fatal("新建 Session 的计费数据应全部为零")
	}
	if s.CreatedAt.IsZero() {
		t.Fatal("CreatedAt 不应为零值")
	}
}
