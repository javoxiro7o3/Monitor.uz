package collector

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// Runner executes an external tool. Tests replace it with canned output.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, exitCode int, err error)

// ExecRunner runs a real command with a timeout. A non-zero exit code is
// returned, not treated as an error (smartctl uses exit bits for status).
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), 0, err
}

// LookTool returns the path of a tool or "" if it is not installed.
func LookTool(name string) string {
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/sbin", "/usr/local/bin", "/opt/smartstorageadmin/ssacli/bin"} {
		p := dir + "/" + name
		if fileExecutable(p) {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func fileExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}
