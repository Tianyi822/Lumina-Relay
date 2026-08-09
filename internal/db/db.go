package db

import (
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // 注册纯 Go sqlite 驱动
)

// sqlitePragmas 是附加到每个 DSN 的 SQLite pragma 配置。
//
// busy_timeout=5000：写锁竞争时等待最多 5 秒再返回 SQLITE_BUSY，
// 而非默认的立即失败。session 中间件每请求一次 UPDATE 会产生写并发，
// 无此项会导致并发写请求间歇性失败。
//
// journal_mode=WAL：write-ahead logging，允许读写并发（读不阻塞写、写不阻塞读），
// 显著提升并发吞吐，代价是多了 -wal/-shm 辅助文件（数据目录需可写）。
//
// foreign_keys=ON：强制外键约束（accounts↔devices↔blocks），防御性兜底。
const sqlitePragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate"

// 连接池上限：modernc sqlite 每个连接是独立文件句柄与连接状态；写操作被
// _txlock=immediate 全局串行化，无限池在写突发下只会无界消耗 fd（macOS
// 默认软限制 256）而不会提升吞吐。MaxOpenConns 控峰值、MaxIdleConns 控常驻。
const (
	maxOpenConns = 32
	maxIdleConns = 8
)

// withPragmas 给原始 DSN（通常是文件路径）附加 pragma 查询参数。
// 若 DSN 已含查询串则用 & 拼接，否则用 ? 起始。
func withPragmas(dsn string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + sqlitePragmas
	}
	return dsn + "?" + sqlitePragmas
}

// Open 打开指定 DSN 的 SQLite 数据库，返回 *sqlx.DB。
// dsn 为 modernc sqlite 驱动接受的连接字符串（通常是数据库文件路径）。
// 自动附加 busy_timeout/WAL/foreign_keys pragma（见 sqlitePragmas）。
func Open(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite", withPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("打开数据库：%w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	return db, nil
}
