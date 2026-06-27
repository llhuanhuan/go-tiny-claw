package permissions

import (
	"os"
	"time"
)

// readFile 读取文件内容
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// getFileModTime 获取文件修改时间
func getFileModTime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}
