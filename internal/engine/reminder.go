// internal/engine/reminder.go
package engine

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ReminderInjector 负责在运行时监控上下文，并在模型陷入执念时动态注入强力打断信息
type ReminderInjector struct {
	// 用于记录连续失败的工具调用指纹 (ToolName + Arguments 的 Hash)
	consecutiveFailures map[string]int
}

func NewReminderInjector() *ReminderInjector {
	return &ReminderInjector{
		consecutiveFailures: make(map[string]int),
	}
}

// generateFingerprint 生成工具调用的"语义指纹"。
//
// 核心思路：不改 Hash 算法，改"喂给 Hash 的原料"。
// 在计算 MD5 之前，先对 JSON 参数做一次 Canonicalization（语义规范化），
// 消除大模型常见的"小聪明"式差异：
//
//	"/tmp/a.txt"   vs "/tmp/a.txt "  → 尾部空格（TrimSpace）
//	"/tmp/a.txt"   vs "/tmp//a.txt"  → 多余斜杠（filepath.Clean）
//	"/tmp/a.txt"   vs "./../tmp/a.txt" → 相对路径（filepath.Abs + Clean）
//	"1"            vs 1              → JSON 类型差异（统一 float64）
//	"ls -la"       vs "ls  -la  "   → bash 空格差异
func generateFingerprint(toolName string, args []byte) string {
	canonical := canonicalizeArgs(toolName, args)
	hasher := md5.New()
	hasher.Write([]byte(toolName))
	hasher.Write(canonical)
	return hex.EncodeToString(hasher.Sum(nil))
}

// ═══════════════════════════════════════════════════════════════
// 参数规范化层（Canonicalization Layer）
// ═══════════════════════════════════════════════════════════════

// multiSpaceRe 匹配连续的空白字符（用于 bash 命令规范化）
var multiSpaceRe = regexp.MustCompile(`\s{2,}`)

// canonicalizeArgs 将工具参数 JSON 字节流规范化为语义等价的标准形式。
//
// 策略：
//  1. 解析 JSON → 递归规范化每个值 → 重新序列化
//  2. 字符串值：TrimSpace + filepath.Clean（路径感知）
//  3. 数值：统一为 float64（消除 1 vs 1.0 差异）
//  4. bash 工具：额外做命令级规范化（消除空格、尾部重定向差异）
func canonicalizeArgs(toolName string, args []byte) []byte {
	var raw interface{}
	if err := json.Unmarshal(args, &raw); err != nil {
		// JSON 解析失败（可能是裸字符串等非标准格式），原样返回
		return args
	}

	normalized := normalizeValue(raw)

	if m, ok := normalized.(map[string]interface{}); ok {
		if toolName == "bash" {
			// bash 工具：command 字段走专用规范化，不做 filepath.Clean
			if cmd, exists := m["command"]; exists {
				if cmdStr, ok := cmd.(string); ok {
					m["command"] = normalizeBashCommand(cmdStr)
				}
			}
		} else {
			// 非 bash 工具：对所有字符串值做路径规范化
			cleanPathsInMap(m)
		}
	}

	result, err := json.Marshal(normalized)
	if err != nil {
		return args // 降级：规范化失败时回退到原始参数
	}
	return result
}

// normalizeValue 递归规范化一个 JSON 值。
//
// 规范化规则：
//   - 字符串 → TrimSpace + cleanPath（跨平台路径规范化）
//   - 数值   → 统一为 int64/float64（消除 1 vs 1.0 差异）
//   - 布尔/null → 保持不变
//   - 对象   → 递归规范化每个 value（Go map 遍历顺序稳定，同数据同输出）
//   - 数组   → 递归规范化每个元素
func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		// 只做 TrimSpace；路径规范化由 cleanPathsInMap 单独处理
		// 避免 filepath.Clean 误伤 bash 命令中的 > /dev/null 等语法
		return strings.TrimSpace(val)

	case float64:
		// 整数规范化：如果值等于其截断整数，返回整数形式
		// 这样 1.0 和 1 在 JSON 序列化时都会变成 1
		if val == math.Trunc(val) {
			return int64(val)
		}
		return val

	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			out[k] = normalizeValue(v2)
		}
		return out

	case []interface{}:
		out := make([]interface{}, len(val))
		for i, v2 := range val {
			out[i] = normalizeValue(v2)
		}
		return out

	case bool, nil:
		return val

	default:
		return val
	}
}

// cleanPathsInMap 对 map 中的字符串值做路径规范化。
// 跳过名为 "command" 的字段（bash 命令由 normalizeBashCommand 单独处理）。
func cleanPathsInMap(m map[string]interface{}) {
	for k, v := range m {
		if str, ok := v.(string); ok {
			if k != "command" {
				m[k] = filepath.ToSlash(filepath.Clean(str))
			}
		}
	}
}

// normalizeBashCommand 对 bash 命令字符串做额外规范化。
//
// 捕获以下大模型"小聪明"：
//   - "ls  -la"    → "ls -la"（多余空格）
//   - "ls -la  "   → "ls -la"（尾部空格，TrimSpace 已处理）
//   - "ls -la > /dev/null" vs "ls -la >/dev/null"（重定向格式差异）
//   - "FOO=bar ls" → "ls"（忽略环境变量前缀）
func normalizeBashCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)

	// 去除环境变量前缀（如 "FOO=bar BAZ=qux ls -la" → "ls -la"）
	// 规则：如果第一个 token 包含 =，则跳过所有 KEY=VALUE 前缀
	words := strings.Fields(cmd)
	start := 0
	for i, w := range words {
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
			start = i + 1
			continue
		}
		break
	}
	if start > 0 && start < len(words) {
		words = words[start:]
	}

	// 统一空格：连续空白 → 单个空格
	normalized := strings.Join(words, " ")
	normalized = multiSpaceRe.ReplaceAllString(normalized, " ")

	// 统一重定向格式："> /dev/null" → ">/dev/null"
	normalized = strings.ReplaceAll(normalized, "> /", ">/")
	normalized = strings.ReplaceAll(normalized, "2> /", "2>/")
	normalized = strings.ReplaceAll(normalized, "&> /", "&>/")

	return strings.TrimSpace(normalized)
}

// ═══════════════════════════════════════════════════════════════
// CheckAndInject — 死循环诊断与打断
// ═══════════════════════════════════════════════════════════════

// CheckAndInject 分析本轮的执行结果，决定是否要在 Context 尾部追加 Reminder
// 返回的 schema.Message 将作为最新的用户输入，强制大模型优先阅读。
func (r *ReminderInjector) CheckAndInject(lastToolCall schema.ToolCall, lastResult schema.ToolResult) *schema.Message {
	fingerprint := generateFingerprint(lastToolCall.Name, lastToolCall.Arguments)

	// 如果工具执行成功，说明 Agent 在这条路径上走通了，清空所有失败计数器
	if !lastResult.IsError {
		r.consecutiveFailures = make(map[string]int)
		return nil
	}

	// 如果执行失败，累加该特征的失败次数
	r.consecutiveFailures[fingerprint]++
	failCount := r.consecutiveFailures[fingerprint]

	log.Printf("[Reminder] 监控到工具 %s 执行失败，该参数特征连续失败次数: %d\n", lastToolCall.Name, failCount)

	// 【驾驭底线】：触发死循环打断机制！
	// 我们设定阈值为 3 次。如果大模型连续 3 次都在同一个地方跌倒，必须强行打断它的局部执念。
	if failCount >= 3 {
		log.Println("[Reminder] ⚠️ 触发死循环干预！注入强力修正指令。")

		// 构造一条极其严厉的行动指南
		nudgeMsg := fmt.Sprintf(`[SYSTEM REMINDER 警告]
你似乎陷入了死循环。你刚刚连续 %d 次使用相同的参数调用了 '%s' 工具，并且都失败了。
请立即停止这种无效的重试！你的注意力被当前的报错过度吸引了。
你需要：
1. 停止猜测参数。跳出当前的局部思维。
2. 彻底改变你的策略。
3. 如果你确实无法通过系统工具解决当前问题，请直接结束任务并向用户说明你需要什么人工帮助，而不是继续盲目消耗 API 资源尝试。`, failCount, lastToolCall.Name)

		return &schema.Message{
			Role:    schema.RoleUser, // 【核心】必须是 RoleUser，以保证在下一次 API 请求时拥有最高的近因效应权重
			Content: nudgeMsg,
		}
	}

	return nil
}
