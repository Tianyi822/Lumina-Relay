package config

import (
	"os"
	"path/filepath"
)

const dataDirName = ".lumina-relay"

// DataDir 返回用户数据目录（~/.lumina-relay）。
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dataDirName), nil
}

// DefaultConfigPath 返回默认配置文件路径（~/.lumina-relay/config.yaml）。
func DefaultConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// DefaultLogPath 返回默认日志文件路径（~/.lumina-relay/logs/lumina-relay.log）。
func DefaultLogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "lumina-relay.log"), nil
}
