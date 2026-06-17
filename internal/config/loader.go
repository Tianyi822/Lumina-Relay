package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"lumina-relay/internal/logger"
)

// Load 从 path 读取并解析配置。
//   - 文件不存在：写入默认配置到 path，记录 info（非错误）。
//   - 文件存在但解析失败：返回 error，main 应退出（格式错可能波及业务配置）。
//   - 成功：合并默认值（缺失字段补默认）→ env 覆盖 → 返回。
//
// 调用时 zap 未就绪，日志走 slog 兜底（需 main 先 InitBootstrap）。
func Load(path string) (AppConfig, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := writeDefaultConfig(path); err != nil {
				return cfg, err
			}
			logger.Info("配置文件不存在，已写入默认配置", logger.String("path", path))
			return applyEnv(cfg), nil
		}
		// 其他读取错误（权限等）
		return cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return applyEnv(cfg), nil
}

// writeDefaultConfig 将 Default() 序列化写入 path，并创建父目录。
func writeDefaultConfig(path string) error {
	cfg := Default()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化默认配置失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入默认配置失败: %w", err)
	}
	return nil
}

// LoadDefault 从 ~/.lumina-relay/config.yaml 加载配置。
func LoadDefault() (AppConfig, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return AppConfig{}, fmt.Errorf("无法确定配置路径: %w", err)
	}
	return Load(path)
}

// applyEnv 对各配置段应用环境变量覆盖（env 优先级最高）。
func applyEnv(cfg AppConfig) AppConfig {
	cfg.Log = logger.ApplyEnvOverrides(cfg.Log)
	if v := os.Getenv("LUMINA_SERVER_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("LUMINA_SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	return cfg
}
