//go:build windows

package cursor

import (
	"context"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Windows ships SQLite in winsqlite3.dll. No CGO, copied database, credential
// export, Python runtime, or write access to the editor's database is needed.
func readDesktopToken(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dll := syscall.NewLazyDLL("winsqlite3.dll")
	if err := dll.Load(); err != nil {
		return "", fmt.Errorf("Windows SQLite unavailable")
	}
	open := dll.NewProc("sqlite3_open_v2")
	closeDB := dll.NewProc("sqlite3_close")
	prepare := dll.NewProc("sqlite3_prepare_v2")
	finalize := dll.NewProc("sqlite3_finalize")
	step := dll.NewProc("sqlite3_step")
	column := dll.NewProc("sqlite3_column_text")
	size := dll.NewProc("sqlite3_column_bytes")
	busy := dll.NewProc("sqlite3_busy_timeout")
	for _, proc := range []*syscall.LazyProc{open, closeDB, prepare, finalize, step, column, size, busy} {
		if proc.Find() != nil {
			return "", fmt.Errorf("Windows SQLite API unavailable")
		}
	}
	name, err := syscall.BytePtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("invalid database path")
	}
	var db uintptr
	code, _, _ := open.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&db)), 1, 0) // SQLITE_OPEN_READONLY
	runtime.KeepAlive(name)
	if db != 0 {
		defer closeDB.Call(db)
	}
	if code != 0 {
		return "", fmt.Errorf("Cursor database unavailable")
	}
	busy.Call(db, 1000)
	query, _ := syscall.BytePtrFromString("SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'")
	var statement uintptr
	code, _, _ = prepare.Call(db, uintptr(unsafe.Pointer(query)), ^uintptr(0), uintptr(unsafe.Pointer(&statement)), 0)
	runtime.KeepAlive(query)
	if statement != 0 {
		defer finalize.Call(statement)
	}
	if code != 0 {
		return "", fmt.Errorf("Cursor database schema unavailable")
	}
	code, _, _ = step.Call(statement)
	if code != 100 {
		return "", fmt.Errorf("Cursor login missing")
	} // SQLITE_ROW
	n, _, _ := size.Call(statement, 0)
	ptr, _, _ := column.Call(statement, 0)
	if ptr == 0 || n == 0 || n > 1024*1024 {
		return "", fmt.Errorf("invalid Cursor login")
	}
	// Copy from native SQLite memory using a native pointer-sized address. This
	// avoids constructing a Go pointer from an integer returned by the DLL.
	out := make([]byte, int(n))
	copyMemory := syscall.NewLazyDLL("kernel32.dll").NewProc("ReadProcessMemory")
	if copyMemory.Find() != nil {
		return "", fmt.Errorf("native SQLite copy unavailable")
	}
	ok, _, _ := copyMemory.Call(^uintptr(0), ptr, uintptr(unsafe.Pointer(&out[0])), n, 0)
	runtime.KeepAlive(out)
	if ok == 0 {
		return "", fmt.Errorf("native SQLite read failed")
	}
	return string(out), nil
}
