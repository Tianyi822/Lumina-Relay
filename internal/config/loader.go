package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"

	"lumina-relay/internal/logger"
)

// Load 从 path 读取并解析配置。
//   - 文件不存在：用默认值启动，记录 info（非错误）。
//   - 文件存在但解析失败：返回 error，main 应退出（格式错可能波及业务配置）。
//   - 成功：合并默认值（缺失字段补默认）→ env 覆盖 → 返回。
//
// 调用时 zap 未就绪，日志走 slog 兜底（需 main 先 InitBootstrap）。
func Load(path string) (AppConfig, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("配置文件不存在，使用默认配置", logger.String("path", path))
			// 文件不存在不算错误，仍走 env 覆盖 + 默认值
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
