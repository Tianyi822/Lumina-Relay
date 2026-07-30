// Package db 提供 lumina-relay 的 SQLite 数据访问层。
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite" // 注册纯 Go sqlite 驱动
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// MigrateUp 对指定 DSN 的 SQLite 数据库执行全部向上迁移。
// dsn 为 modernc sqlite 驱动接受的连接字符串（通常是数据库文件路径）。
// 若数据库已是最新版本，返回 nil（忽略 ErrNoChange）。
func MigrateUp(dsn string) error {
	db, err := sql.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return fmt.Errorf("打开数据库：%w", err)
	}
	defer db.Close()

	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("初始化迁移 source：%w", err)
	}

	drv, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("初始化迁移 driver：%w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	if err != nil {
		return fmt.Errorf("创建 migrate 实例：%w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("执行迁移：%w", err)
	}
	return nil
}
