package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"lumina-relay/internal/logger"
)

// config 测试二进制中 logger 的 TestMain 不会运行，
// 因此显式调用 logger.InitBootstrap() 使包级日志函数不 nil panic。
func TestMain(m *testing.M) {
	logger.InitBootstrap()
	os.Exit(m.Run())
}

func writeTempYAML(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写临时配置失败：%v", err)
	}
	return path
}

func TestLoad_FileNotExists_UsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("文件不存在不应报错：%v", err)
	}
	if cfg.Server.Port != 8443 {
		t.Fatalf("应回退默认 Port，got %d", cfg.Server.Port)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("应回退默认 Level，got %q", cfg.Log.Level)
	}
}

func TestLoad_ValidFile(t *testing.T) {
	yaml := `
log:
  level: debug
  file:
    enabled: false
    path: /var/log/app.log
server:
  port: 9000
  host: 127.0.0.1
`
	path := writeTempYAML(t, "config.yaml", yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if cfg.Log.Level != "debug" {
		t.Fatalf("Level = %q", cfg.Log.Level)
	}
	if cfg.Log.File.Enabled {
		t.Fatal("File.Enabled 应为 false")
	}
	if cfg.Log.File.Path != "/var/log/app.log" {
		t.Fatalf("Path = %q", cfg.Log.File.Path)
	}
	if cfg.Server.Port != 9000 || cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("Server = %+v", cfg.Server)
	}
}

func TestLoad_PartialFile_KeepsDefaultsForMissingFields(t *testing.T) {
	yaml := `
server:
  port: 7777
`
	path := writeTempYAML(t, "partial.yaml", yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if cfg.Server.Port != 7777 {
		t.Fatalf("Port 应被覆盖为 7777，got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("缺失 Host 应保留默认，got %q", cfg.Server.Host)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("缺失 log 段应保留默认 Level，got %q", cfg.Log.Level)
	}
}

func TestLoad_MalformedYAML_ReturnsError(t *testing.T) {
	// 用真实换行（不是字面 \n），保证 yaml 解析失败
	path := writeTempYAML(t, "bad.yaml", "log:\n  level: [unterminated")
	_, err := Load(path)
	if err == nil {
		t.Fatal("语法错误应返回 error")
	}
}

func TestLoad_EnvOverridesServer(t *testing.T) {
	t.Setenv("LUMINA_SERVER_PORT", "9999")
	t.Setenv("LUMINA_SERVER_HOST", "1.2.3.4")
	path := writeTempYAML(t, "c.yaml", "server:\n  port: 8000\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Fatalf("Port 应被 env 覆盖为 9999，got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "1.2.3.4" {
		t.Fatalf("Host 应被 env 覆盖，got %q", cfg.Server.Host)
	}
}

func TestLoad_EnvInvalidPortIgnored(t *testing.T) {
	t.Setenv("LUMINA_SERVER_PORT", "not-a-number")
	path := writeTempYAML(t, "c.yaml", "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if cfg.Server.Port != 8443 {
		t.Fatalf("非法端口应被忽略，got %d", cfg.Server.Port)
	}
}

func TestLoad_EnvOverridesLog(t *testing.T) {
	t.Setenv("LUMINA_LOG_LEVEL", "warn")
	path := writeTempYAML(t, "c.yaml", "log:\n  level: debug\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if cfg.Log.Level != "warn" {
		t.Fatalf("Log.Level 应被 env 覆盖为 warn，got %q", cfg.Log.Level)
	}
}

// 黄金配置文件冒烟：仓库根的 config.yaml 必须存在且是合法 YAML。
// Red 阶段：文件尚未创建 → ReadFile 失败 → 测试失败。
// 注意：测试运行目录是 internal/config/，仓库根在相对路径 ../../。
func TestGoldenConfigFile_IsValidYAML(t *testing.T) {
	path := filepath.Join("..", "..", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config.yaml 不存在或不可读（应随仓库提交）：%v", err)
	}
	var cfg AppConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config.yaml 解析失败：%v", err)
	}
	// 默认配置的关键字段（与 Default() 一致）
	if cfg.Server.Port != 8443 || cfg.Log.Level != "info" || !cfg.Log.File.Enabled {
		t.Fatalf("config.yaml 值与预期不符：%+v", cfg)
	}
}
