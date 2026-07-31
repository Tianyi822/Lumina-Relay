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

// DefaultDBPath 返回默认数据库文件路径（~/.lumina-relay/db/relay.db）。
func DefaultDBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "db", "relay.db"), nil
}

// DefaultJWTSecretPath 返回 JWT 签名密钥文件路径（~/.lumina-relay/jwt_secret）。
func DefaultJWTSecretPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jwt_secret"), nil
}

// DefaultBlocksDir 返回默认密文块存储目录（~/.lumina-relay/blocks）。
func DefaultBlocksDir() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "blocks"), nil
}
