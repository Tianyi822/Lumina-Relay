package db

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // 注册纯 Go sqlite 驱动
)

// Open 打开指定 DSN 的 SQLite 数据库，返回 *sqlx.DB。
// dsn 为 modernc sqlite 驱动接受的连接字符串（通常是数据库文件路径）。
func Open(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库：%w", err)
	}
	return db, nil
}
