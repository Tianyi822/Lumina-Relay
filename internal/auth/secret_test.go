package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrGenerateSecret_FirstRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt_secret")

	secret, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("首次生成失败：%v", err)
	}
	if len(secret) != SecretLen {
		t.Fatalf("密钥长度 %d，期望 %d", len(secret), SecretLen)
	}

	// 验证文件已创建且权限正确
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密钥文件未创建：%v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("文件权限 %o，期望 0600", info.Mode().Perm())
	}
}

func TestLoadOrGenerateSecret_Reuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt_secret")

	// 首次生成
	s1, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("首次生成失败：%v", err)
	}

	// 第二次调用应返回相同密钥
	s2, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("第二次读取失败：%v", err)
	}

	if string(s1) != string(s2) {
		t.Fatal("两次调用返回不同密钥")
	}
}

func TestLoadOrGenerateSecret_CorruptedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt_secret")

	// 写入长度错误的内容
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatalf("写入损坏文件失败：%v", err)
	}

	secret, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("修复损坏文件失败：%v", err)
	}
	if len(secret) != SecretLen {
		t.Fatalf("修复后密钥长度 %d，期望 %d", len(secret), SecretLen)
	}

	// 再次读取应返回新生成的密钥（已覆盖损坏文件）
	s2, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("再次读取失败：%v", err)
	}
	if string(secret) != string(s2) {
		t.Fatal("覆盖损坏文件后密钥不一致")
	}
}

func TestLoadOrGenerateSecret_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jwt_secret")

	// 写入空文件
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("写入空文件失败：%v", err)
	}

	secret, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("处理空文件失败：%v", err)
	}
	if len(secret) != SecretLen {
		t.Fatalf("密钥长度 %d，期望 %d", len(secret), SecretLen)
	}
}

func TestLoadOrGenerateSecret_NonExistentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "jwt_secret")

	secret, err := LoadOrGenerateSecret(path)
	if err != nil {
		t.Fatalf("自动创建目录失败：%v", err)
	}
	if len(secret) != SecretLen {
		t.Fatalf("密钥长度 %d，期望 %d", len(secret), SecretLen)
	}

	// 文件应存在
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("密钥文件未创建：%v", err)
	}
}
