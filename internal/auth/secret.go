package auth

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

// SecretLen 是 HS256 签名密钥的字节数（≥32 满足 HMAC-SHA256 安全要求）。
const SecretLen = 32

// LoadOrGenerateSecret 从 path 读取 JWT 签名密钥；若文件不存在或内容无效，
// 则随机生成并写入 path（权限 0600），此后重启可复用同一密钥。
//
// 容错策略：写入失败不视为致命错误——降级返回内存中的随机密钥，
// 保证本次进程可正常运行（仅下次重启仍需重新生成）。
func LoadOrGenerateSecret(path string) ([]byte, error) {
	// 优先读取已有文件
	if secret, err := os.ReadFile(path); err == nil {
		if len(secret) == SecretLen {
			return secret, nil
		}
		// 文件存在但长度不对：视为损坏，重新生成
	}

	// 生成新密钥
	secret := make([]byte, SecretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("生成 JWT 密钥：%w", err)
	}

	// 确保持久化目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		// 目录创建失败：降级，返回内存密钥
		return secret, nil
	}

	// 写入文件，仅属主可读写
	if err := os.WriteFile(path, secret, 0600); err != nil {
		// 写入失败：降级，返回内存密钥
		return secret, nil
	}

	return secret, nil
}
