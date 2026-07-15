package tools

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// ValidateToolArgs 测试
// =============================================================================

func TestValidateToolArgs_IllegalJSON(t *testing.T) {
	err := ValidateToolArgs(json.RawMessage(`{bad json`), nil)
	if err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
	t.Logf("✅ 非法 JSON 错误: %v", err)
}

func TestValidateToolArgs_ArrayNotObject(t *testing.T) {
	err := ValidateToolArgs(json.RawMessage(`[1,2,3]`), nil)
	if err == nil {
		t.Fatal("数组参数应返回错误")
	}
	t.Logf("✅ 数组参数错误: %v", err)
}

func TestValidateToolArgs_NilSchema(t *testing.T) {
	err := ValidateToolArgs(json.RawMessage(`{"any":"data"}`), nil)
	if err != nil {
		t.Fatalf("无 schema 时应跳过校验: %v", err)
	}
	t.Log("✅ nil schema 跳过校验")
}

func TestValidateToolArgs_RequiredFieldMissing(t *testing.T) {
	schema := map[string]interface{}{
		"required": []interface{}{"path", "content"},
	}
	args := json.RawMessage(`{"path":"test.txt"}`)
	err := ValidateToolArgs(args, schema)
	if err == nil {
		t.Fatal("缺少 required 字段应返回错误")
	}
	t.Logf("✅ 缺少必填参数: %v", err)
}

func TestValidateToolArgs_RequiredFieldPresent(t *testing.T) {
	schema := map[string]interface{}{
		"required": []interface{}{"path"},
	}
	args := json.RawMessage(`{"path":"test.txt","extra":"ok"}`)
	err := ValidateToolArgs(args, schema)
	if err != nil {
		t.Fatalf("required 字段齐全时应通过: %v", err)
	}
	t.Log("✅ required 字段齐全通过")
}

func TestValidateToolArgs_ExtraFieldsAllowed(t *testing.T) {
	schema := map[string]interface{}{
		"required": []interface{}{"path"},
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
	}
	// LLM 传了 schema 未定义的 extra_field，应允许
	args := json.RawMessage(`{"path":"test.txt","extra_field":"allowed"}`)
	err := ValidateToolArgs(args, schema)
	if err != nil {
		t.Fatalf("额外参数应被允许: %v", err)
	}
	t.Log("✅ 额外参数被允许")
}

// =============================================================================
// checkType 测试 — 每种类型匹配 + 不匹配
// =============================================================================

func TestCheckType_String(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"字符串匹配", "hello", false},
		{"数字不匹配", float64(42), true},
		{"布尔不匹配", true, true},
		{"数组不匹配", []interface{}{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "string")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(string) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_Integer(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"整数匹配", float64(42), false},
		{"浮点数不匹配", float64(3.14), true},
		{"字符串不匹配", "42", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "integer")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(integer) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_Number(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"整数float64", float64(42), false},
		{"浮点float64", float64(3.14), false},
		{"字符串不匹配", "3.14", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "number")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(number) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_Boolean(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"true 匹配", true, false},
		{"false 匹配", false, false},
		{"字符串不匹配", "true", true},
		{"数字不匹配", float64(1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "boolean")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(boolean) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_Array(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"数组匹配", []interface{}{1, 2}, false},
		{"空数组匹配", []interface{}{}, false},
		{"字符串不匹配", "[]", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "array")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(array) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_Object(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"对象匹配", map[string]interface{}{"a": 1}, false},
		{"空对象匹配", map[string]interface{}{}, false},
		{"字符串不匹配", "{}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkType("arg", tt.value, "object")
			if (err != nil) != tt.wantErr {
				t.Errorf("checkType(object) = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckType_UnknownType(t *testing.T) {
	// 未知类型应跳过校验（不报错）
	err := checkType("arg", "anything", "unknown_type")
	if err != nil {
		t.Fatalf("未知类型应跳过: %v", err)
	}
	t.Log("✅ 未知类型跳过校验")
}

// =============================================================================
// 端到端集成测试 — 模拟真实工具 Schema
// =============================================================================

func TestValidateToolArgs_RealWorld_ReadFile(t *testing.T) {
	// 模拟 read_file 工具的 schema
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"path"},
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
			"offset": map[string]interface{}{"type": "integer"},
			"limit": map[string]interface{}{"type": "integer"},
		},
	}

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"正常参数", `{"path":"/tmp/test.go"}`, false},
		{"带可选参数", `{"path":"/tmp/test.go","offset":10,"limit":100}`, false},
		{"缺少 path", `{"offset":10}`, true},
		{"path 类型错误", `{"path":123}`, true},
		{"offset 类型错误", `{"path":"/tmp","offset":3.14}`, true},
		{"非法 JSON", `not json`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolArgs(json.RawMessage(tt.args), schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToolArgs() = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				t.Logf("  错误信息: %v", err)
			}
		})
	}
}

func TestValidateToolArgs_RealWorld_Bash(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"command"},
		"properties": map[string]interface{}{
			"command":   map[string]interface{}{"type": "string"},
			"workdir":   map[string]interface{}{"type": "string"},
			"background": map[string]interface{}{"type": "boolean"},
		},
	}

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{"正常命令", `{"command":"ls -la"}`, false},
		{"带 workdir", `{"command":"ls","workdir":"/tmp"}`, false},
		{"带 background", `{"command":"sleep 10","background":true}`, false},
		{"缺少 command", `{"workdir":"/tmp"}`, true},
		{"command 类型错误", `{"command":123}`, true},
		{"background 类型错误", `{"command":"ls","background":"yes"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateToolArgs(json.RawMessage(tt.args), schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateToolArgs() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
