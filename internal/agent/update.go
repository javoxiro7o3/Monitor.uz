package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"monitor.uz/internal/model"
)

// applyUpdate downloads the ordered binary, verifies its SHA-256 and that
// it runs and reports the expected version, then hands it to OnUpdate.
func applyUpdate(ctx context.Context, c *Client, cfg Config, o *model.UpdateOrder, log *slog.Logger) error {
	if !strings.HasPrefix(o.URL, "/dl/") || len(o.SHA256) != 64 {
		return errors.New("invalid update order")
	}
	log.Info("downloading update", "version", o.Version)
	tmp := filepath.Join(cfg.StateDir, "monitor-agent.download")
	defer os.Remove(tmp)
	if err := c.Download(ctx, o.URL, tmp, 200<<20); err != nil {
		return err
	}
	sum, err := fileSHA256(tmp)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, o.SHA256) {
		return fmt.Errorf("checksum mismatch: got %s", sum)
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, tmp, "version").Output()
	if err != nil {
		return fmt.Errorf("new binary does not run: %w", err)
	}
	if v := strings.TrimSpace(string(out)); v != o.Version {
		return fmt.Errorf("new binary reports version %q, expected %q", v, o.Version)
	}
	if err := cfg.OnUpdate(tmp, o); err != nil {
		return err
	}
	log.Info("update installed, restarting", "version", o.Version)
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReplaceBinary atomically swaps target with newBin, keeping the old one
// as target+".prev" for manual rollback.
func ReplaceBinary(target, newBin string) error {
	dir := filepath.Dir(target)
	staged := filepath.Join(dir, ".monitor-agent.new")
	in, err := os.Open(newBin)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(staged, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(staged)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(staged)
		return err
	}
	_ = os.Remove(target + ".prev")
	if err := os.Link(target, target+".prev"); err != nil {
		// Not fatal: rollback copy is a convenience.
		_ = err
	}
	return os.Rename(staged, target)
}
