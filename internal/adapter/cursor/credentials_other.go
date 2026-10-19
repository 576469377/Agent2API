//go:build !windows

package cursor

import (
	"context"
	"fmt"
	"os/exec"
)

func readDesktopToken(ctx context.Context, path string) (string, error) {
	const script = `import sqlite3,sys,pathlib
from contextlib import closing
with closing(sqlite3.connect(pathlib.Path(sys.argv[1]).resolve().as_uri()+'?mode=ro',uri=True)) as c:
 r=c.execute("SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'").fetchone()
 if r: sys.stdout.write(r[0])`
	for _, name := range []string{"python3", "python"} {
		if program, err := exec.LookPath(name); err == nil {
			out, err := exec.CommandContext(ctx, program, "-c", script, path).Output()
			if err != nil || len(out) > 1024*1024 {
				return "", fmt.Errorf("Cursor read-only SQLite read failed")
			}
			return string(out), nil
		}
	}
	return "", fmt.Errorf("Python 3 is required to read Cursor SQLite on this platform")
}
