// internal/memory/storage.go
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Storage 负责记忆的持久化存储。
// 使用 JSONL 格式，每个会话一个文件。
type Storage struct {
	workDir string
	mu      sync.RWMutex
}

// NewStorage 创建一个存储实例。
func NewStorage(workDir string) *Storage {
	return &Storage{workDir: workDir}
}

// summariesDir 返回摘要存储目录。
func (s *Storage) summariesDir() string {
	return filepath.Join(s.workDir, ".claw", "summaries")
}

// memoryDir 返回长期记忆存储目录。
func (s *Storage) memoryDir() string {
	return filepath.Join(s.workDir, ".claw", "memory")
}

// summaryPath 返回指定会话的摘要文件路径。
func (s *Storage) summaryPath(sessionID string) string {
	return filepath.Join(s.summariesDir(), SanitizeFilename(sessionID)+".jsonl")
}

// memoryPath 返回指定会话的长期记忆文件路径。
func (s *Storage) memoryPath(sessionID string) string {
	return filepath.Join(s.memoryDir(), SanitizeFilename(sessionID)+".jsonl")
}

// globalMemoryPath 返回全局记忆文件路径。
func (s *Storage) globalMemoryPath() string {
	return filepath.Join(s.memoryDir(), "_global.jsonl")
}

// SanitizeFilename 将文件名中的非法字符替换为下划线。
// Windows 上 ':' 是 NTFS ADS 分隔符。
func SanitizeFilename(id string) string {
	replacer := map[rune]bool{
		':': true, '*': true, '?': true, '"': true,
		'<': true, '>': true, '|': true, '/': true, '\\': true,
	}
	result := make([]rune, 0, len(id))
	for _, r := range id {
		if replacer[r] {
			result = append(result, '_')
		} else {
			result = append(result, r)
		}
	}
	return string(result)
}

// SaveSummary 保存摘要到磁盘（追加模式）。
func (s *Storage) SaveSummary(sessionID string, summary Summary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.summariesDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建摘要目录失败: %w", err)
	}

	path := s.summaryPath(sessionID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开摘要文件失败: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("序列化摘要失败: %w", err)
	}

	writer := bufio.NewWriter(f)
	writer.Write(data)
	writer.WriteByte('\n')
	return writer.Flush()
}

// LoadSummaries 加载指定会话的所有摘要。
func (s *Storage) LoadSummaries(sessionID string) ([]Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.summaryPath(sessionID)
	return readJSONL[Summary](path)
}

// SaveFact 保存一条长期记忆（追加模式）。
func (s *Storage) SaveFact(sessionID string, fact Fact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.memoryDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建记忆目录失败: %w", err)
	}

	path := s.memoryPath(sessionID)
	return appendFact(path, fact)
}

// SaveGlobalFact 保存一条全局记忆。
func (s *Storage) SaveGlobalFact(fact Fact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.memoryDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建记忆目录失败: %w", err)
	}

	path := s.globalMemoryPath()
	return appendFact(path, fact)
}

// LoadFacts 加载指定会话的长期记忆（含全局记忆）。
func (s *Storage) LoadFacts(sessionID string) ([]Fact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 先加载全局记忆
	globalFacts, _ := readJSONL[Fact](s.globalMemoryPath())

	// 再加载会话级记忆
	sessionFacts, _ := readJSONL[Fact](s.memoryPath(sessionID))

	// 合并
	result := make([]Fact, 0, len(globalFacts)+len(sessionFacts))
	result = append(result, globalFacts...)
	result = append(result, sessionFacts...)
	return result, nil
}

// appendFact 追加一条事实到文件。
func appendFact(path string, fact Fact) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开记忆文件失败: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("序列化事实失败: %w", err)
	}

	writer := bufio.NewWriter(f)
	writer.Write(data)
	writer.WriteByte('\n')
	return writer.Flush()
}

// readJSONL 从 JSONL 文件读取所有记录。
func readJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 文件不存在是正常情况
		}
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	var result []T
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var item T
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			log.Printf("[Memory] ⚠️ 跳过无法解析的行: %v\n", err)
			continue
		}
		result = append(result, item)
	}
	return result, scanner.Err()
}
