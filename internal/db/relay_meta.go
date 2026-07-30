package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
)

const (
	// instanceIDByteLen 是 instanceId 的随机字节长度。
	// 32 字节经无填充 base64url 编码，既有足够熵又可安全放入 JSON 与签名 transcript。
	instanceIDByteLen = 32

	sqlGetInstanceID = `SELECT value
FROM relay_meta
WHERE key = 'instance_id'`

	sqlInsertInstanceID = `INSERT OR IGNORE INTO relay_meta (key, value)
VALUES ('instance_id', ?)`
)

// GetOrCreateInstanceID 返回当前 Relay 数据库的持久 instanceId。
//
// 空数据库首次调用时生成 32 字节随机值并以无填充 base64url 编码后写入 relay_meta；
// 后续调用和进程重启均读取同一值。并发首次调用依靠 singleton 主键与
// INSERT OR IGNORE 收敛到唯一持久值。
func (q *Queries) GetOrCreateInstanceID(ctx context.Context) (string, error) {
	instanceID, err := q.getInstanceID(ctx)
	if err == nil {
		return instanceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	raw := make([]byte, instanceIDByteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 Relay instanceId：%w", err)
	}
	candidate := base64.RawURLEncoding.EncodeToString(raw)

	if _, err := q.db.ExecContext(ctx, sqlInsertInstanceID, candidate); err != nil {
		return "", fmt.Errorf("持久化 Relay instanceId：%w", err)
	}

	// 并发初始化时本次 candidate 可能因 singleton 冲突被忽略，
	// 因此始终读回数据库中的获胜值，而不是直接返回 candidate。
	instanceID, err = q.getInstanceID(ctx)
	if err != nil {
		return "", err
	}
	return instanceID, nil
}

// getInstanceID 读取已经持久化的 instanceId；尚未初始化时保留 sql.ErrNoRows
// 在错误链中，供 GetOrCreateInstanceID 判定。
func (q *Queries) getInstanceID(ctx context.Context) (string, error) {
	var instanceID string
	if err := q.db.QueryRowxContext(ctx, sqlGetInstanceID).Scan(&instanceID); err != nil {
		return "", fmt.Errorf("读取 Relay instanceId：%w", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(instanceID)
	if err != nil || len(raw) != instanceIDByteLen ||
		base64.RawURLEncoding.EncodeToString(raw) != instanceID {
		return "", fmt.Errorf("读取 Relay instanceId：持久值格式非法")
	}
	return instanceID, nil
}
