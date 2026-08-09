package logger

import "runtime/debug"

// Recover 在 defer 中捕获 panic。
// 切回兜底 logger（不信任当前 zap 实例），记录堆栈后重新 panic 让程序退出。
//
// 约束：Recover 只覆盖其所在 goroutine，且 re-panic 会终止整个进程——仅用于
// 主流程入口（main/runServer 的 fail-fast）。所有关键子 goroutine（WS 读循环、
// 后台 GC 等）必须用 RecoverLogOnly，避免单个子任务崩溃拖垮全进程。
func Recover() {
	if r := recover(); r != nil {
		logPanic("程序发生 panic，即将退出", r)
		// 重新 panic：让 Go runtime 打印标准堆栈到 stderr 并非零退出。
		// 兜底日志已先记录结构化版本，两者互补。
		panic(r)
	}
}

// RecoverLogOnly 在 defer 中捕获 panic，记录结构化日志（含堆栈）后吞掉、
// 不重新 panic。用于子 goroutine：单个子任务的 panic 不应拖垮整个进程，
// 记录后让其余部分继续运行（如后台 GC 下轮重试）。
func RecoverLogOnly() {
	if r := recover(); r != nil {
		logPanic("子 goroutine 发生 panic，已恢复", r)
	}
}

// logPanic 记录 panic 的结构化日志；切回 fallback，不信任当前 zap 实例。
func logPanic(msg string, r any) {
	global = fallback
	Error(msg,
		Any("panic", r),
		String("stack", string(debug.Stack())),
	)
	_ = global.Sync() // 强制 flush 到 stderr
}
