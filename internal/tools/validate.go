package tools

import (
	"encoding/json"
	"fmt"
)

// ValidateToolArgs 校验 LLM 生成的工具参数是否符合 JSON Schema 定义。
// 在 Registry.Execute 调用工具之前执行，防止 malformed JSON 导致 panic。
//
// 校验规则（轻量级，不引入外部依赖）：
//  1. args 必须是合法 JSON
//  2. 必须是 JSON Object（不能是数组/字符串）
//  3. schema 中 required 字段必须存在
//  4. 属性类型基本匹配（string/number/boolean/array/object）
func ValidateToolArgs(args json.RawMessage, schemaMap map[string]interface{}) error {
	// 1. 解析 JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return fmt.Errorf("参数不是合法 JSON: %w", err)
	}

	if schemaMap == nil {
		return nil // 无 schema 定义，跳过校验
	}

	// 2. 检查 required 字段
	required, ok := schemaMap["required"].([]interface{})
	if ok {
		for _, field := range required {
			fieldName, ok := field.(string)
			if !ok {
				continue
			}
			if _, exists := parsed[fieldName]; !exists {
				return fmt.Errorf("缺少必填参数: %s", fieldName)
			}
		}
	}

	// 3. 检查属性类型
	properties, ok := schemaMap["properties"].(map[string]interface{})
	if !ok {
		return nil // 无 properties 定义，跳过类型检查
	}

	for key, value := range parsed {
		propDef, exists := properties[key]
		if !exists {
			continue // 允许额外参数（LLM 可能传了 schema 未定义的字段）
		}
		propMap, ok := propDef.(map[string]interface{})
		if !ok {
			continue
		}
		expectedType, ok := propMap["type"].(string)
		if !ok {
			continue
		}
		if err := checkType(key, value, expectedType); err != nil {
			return err
		}
	}

	return nil
}

// checkType 基本类型检查。
func checkType(key string, value interface{}, expectedType string) error {
	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("参数 %s 期望 string，实际为 %T", key, value)
		}
	case "integer":
		// JSON 解析后数字统一为 float64
		if f, ok := value.(float64); ok {
			if f != float64(int(f)) {
				return fmt.Errorf("参数 %s 期望 integer，实际为浮点数", key)
			}
		} else {
			return fmt.Errorf("参数 %s 期望 integer，实际为 %T", key, value)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("参数 %s 期望 number，实际为 %T", key, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("参数 %s 期望 boolean，实际为 %T", key, value)
		}
	case "array":
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("参数 %s 期望 array，实际为 %T", key, value)
		}
	case "object":
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("参数 %s 期望 object，实际为 %T", key, value)
		}
	}
	return nil
}
