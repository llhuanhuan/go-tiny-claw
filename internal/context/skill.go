package context

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Skill 定义了从 SKILL.md 中解析出的标准化技能结构
type Skill struct {
	Name        string
	Description string
	Body        string // Markdown 正文指令
}

// SkillLoader 负责从本地文件系统中加载并解析符合规范的技能模板。
//
// 遵循 Agent Skills 规范的"渐进式暴露 (Progressive Disclosure)"理念：
//   - LoadAllMetadata() 仅解析 YAML 元数据（轻量，适合注入 System Prompt）
//   - LoadBody(name)    按需加载指定技能的正文（由 read_skill 工具触发）
type SkillLoader struct {
	workDir string
}

func NewSkillLoader(workDir string) *SkillLoader {
	return &SkillLoader{workDir: workDir}
}

// skillBaseDir 返回 SKILL.md 文件的存放根目录
func (s *SkillLoader) skillBaseDir() string {
	return filepath.Join(s.workDir, ".claw", "skills")
}

// findAllSkillFiles 扫描 .claw/skills 目录，返回所有 SKILL.md 的绝对路径。
func (s *SkillLoader) findAllSkillFiles() ([]string, error) {
	baseDir := s.skillBaseDir()
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		return nil, nil // 目录不存在，静默返回空
	}

	var paths []string
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "SKILL.md" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// LoadAllMetadata 扫描所有 SKILL.md，仅解析并返回元数据（Name + Description），
// 不加载正文 Body。适合在 System Prompt 中以极低 Token 成本展示技能索引。
func (s *SkillLoader) LoadAllMetadata() []Skill {
	paths, err := s.findAllSkillFiles()
	if err != nil || len(paths) == 0 {
		return nil
	}

	var skills []Skill
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		skill := parseSkillMD(string(content))
		skill.Body = "" // 渐进式暴露：元数据阶段丢弃正文，节省 Token
		skills = append(skills, skill)
	}
	return skills
}

// LoadBody 根据技能名称查找并返回其完整正文（Markdown 指令部分）。
// 如果找不到匹配的技能，返回 error。
func (s *SkillLoader) LoadBody(name string) (string, error) {
	paths, err := s.findAllSkillFiles()
	if err != nil {
		return "", fmt.Errorf("扫描技能目录失败: %w", err)
	}

	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		skill := parseSkillMD(string(content))
		if skill.Name == name {
			return skill.Body, nil
		}
	}
	return "", fmt.Errorf("未找到名为 '%s' 的技能。可用的技能列表可通过 System Prompt 中的技能索引查看。", name)
}

// AvailableSkillNames 返回所有已安装技能的名称列表，用于错误提示。
func (s *SkillLoader) AvailableSkillNames() []string {
	skills := s.LoadAllMetadata()
	names := make([]string, len(skills))
	for i, sk := range skills {
		names[i] = sk.Name
	}
	return names
}

// LoadAll 一次性加载所有技能的完整内容（元数据 + 正文），格式化为字符串。
//
// Deprecated: 该方法存在 Token 浪费问题（Eager Loading）。
// 新代码应使用 LoadAllMetadata() + LoadBody() 实现渐进式暴露。
// 保留此方法仅为向后兼容。
func (s *SkillLoader) LoadAll() string {
	paths, err := s.findAllSkillFiles()
	if err != nil || len(paths) == 0 {
		return ""
	}

	var skillsBuilder strings.Builder
	skillsBuilder.WriteString("\n### 可用专业技能 (Agent Skills)\n")
	skillsBuilder.WriteString("以下是你拥有的标准化外挂技能，请在符合 description 描述的场景下严格遵循其正文指令：\n\n")

	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		skill := parseSkillMD(string(content))

		skillsBuilder.WriteString(fmt.Sprintf("#### 技能名称: %s\n", skill.Name))
		skillsBuilder.WriteString(fmt.Sprintf("**触发条件**: %s\n\n", skill.Description))
		skillsBuilder.WriteString("**执行指南**:\n")
		skillsBuilder.WriteString(skill.Body)
		skillsBuilder.WriteString("\n\n---\n")
	}

	if skillsBuilder.Len() < 100 {
		return ""
	}
	return skillsBuilder.String()
}

// parseSkillMD 极简解析带有 YAML Frontmatter 的 Markdown 内容
func parseSkillMD(content string) Skill {
	skill := Skill{
		Name:        "Unknown Skill",
		Description: "No description provided.",
		Body:        content, // 默认将全量内容作为 body
	}

	// 简单解析 YAML Frontmatter (以 --- 包裹)
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) == 3 {
			frontmatter := parts[1]
			skill.Body = strings.TrimSpace(parts[2])

			// 逐行提取 metadata
			lines := strings.Split(frontmatter, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					skill.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					skill.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
		}
	}

	return skill
}
