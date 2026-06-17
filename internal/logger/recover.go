package logger

import "runtime/debug"

// Recover 在 defer 中捕获 panic。
// 切回兜底 logger（不信任当前 zap 实例），记录堆栈后重新 panic 让程序退出。
//
// 约束：Recover 只覆盖其所在 goroutine。所有关键子 goroutine（Gin handler、
// 后台任务）入口必须各自 defer Recover()。
func Recover() {
	if r := recover(); r != nil {
		global = fallback // 切回 slog 兜底
		Error("程序发生 panic，即将退出",
			Any("panic", r),
			String("stack", string(debug.Stack())),
		)
		_ = global.Sync() // 强制 flush 到 stderr
		// 重新 panic：让 Go runtime 打印标准堆栈到 stderr 并非零退出。
		// 兜底日志已先记录结构化版本，两者互补。
		panic(r)
	}
}
