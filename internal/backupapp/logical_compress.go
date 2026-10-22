package backupapp

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLogicalCompressThreads = 4
	maxLogicalCompressThreads     = 64
)

// dumpPipeWaitDelay bounds how long cleanup waits for a child's output pipes
// after it exits or is killed; a lingering descendant cannot block a run.
var dumpPipeWaitDelay = 10 * time.Second

var lookPigz = func() (string, error) { return exec.LookPath("pigz") }

// logicalCompressionPolicy validates operator-supplied compression settings.
func logicalCompressionPolicy(cfg config) error {
	switch cfg.LogicalCompressor {
	case "", "auto", "pigz", "gzip":
	default:
		return fmt.Errorf("BACKUP_LOGICAL_COMPRESSOR must be auto, pigz or gzip")
	}
	// Zero means unset (callers predating the setting) and selects the default.
	if cfg.LogicalCompressThreads < 0 || cfg.LogicalCompressThreads > maxLogicalCompressThreads {
		return fmt.Errorf("BACKUP_LOGICAL_COMPRESS_THREADS must be between 1 and %d", maxLogicalCompressThreads)
	}
	return nil
}

func normalizedLogicalCompressThreads(value int) int {
	if value < 1 {
		return defaultLogicalCompressThreads
	}
	return min(value, maxLogicalCompressThreads)
}

// resolveLogicalCompressor returns "pigz" (with its path) or "gzip" (built in).
// auto prefers pigz when installed; an unknown value falls back to the built-in
// compressor; an explicit pigz without the binary is an error.
func resolveLogicalCompressor(cfg config) (string, string, error) {
	switch cfg.LogicalCompressor {
	case "gzip":
		return "gzip", "", nil
	case "pigz":
		path, err := lookPigz()
		if err != nil {
			return "", "", fmt.Errorf("BACKUP_LOGICAL_COMPRESSOR=pigz but pigz is not installed: %w", err)
		}
		return "pigz", path, nil
	case "", "auto":
		if path, err := lookPigz(); err == nil {
			return "pigz", path, nil
		}
		return "gzip", "", nil
	default:
		log.Printf("warning: unknown BACKUP_LOGICAL_COMPRESSOR=%q; using built-in gzip", cfg.LogicalCompressor)
		return "gzip", "", nil
	}
}

// compressWithGzip streams src through the in-process gzip writer.
func compressWithGzip(level int, dst io.Writer, src io.Reader) error {
	gz, err := gzip.NewWriterLevel(dst, level)
	if err != nil {
		return fmt.Errorf("setup gzip writer: %w", err)
	}
	if _, err := io.Copy(gz, src); err != nil {
		_ = gz.Close()
		return fmt.Errorf("stream mysqldump output: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finalize gzip: %w", err)
	}
	return nil
}

// compressWithPigz streams src through an external pigz process into dst. On
// any failure abortSource is called so the producer stops and src unblocks.
func compressWithPigz(ctx context.Context, pigzPath string, level, threads int, dst io.Writer, src io.Reader, abortSource func()) error {
	pigz := exec.CommandContext(ctx, pigzPath, "-c", "-"+strconv.Itoa(level), "-p", strconv.Itoa(threads))
	pigz.WaitDelay = dumpPipeWaitDelay
	var stderr strings.Builder
	pigz.Stderr = &stderr
	in, err := pigz.StdinPipe()
	if err != nil {
		return fmt.Errorf("setup pigz: %w", err)
	}
	out, err := pigz.StdoutPipe()
	if err != nil {
		return fmt.Errorf("setup pigz: %w", err)
	}
	if err := pigz.Start(); err != nil {
		return fmt.Errorf("start pigz: %w", err)
	}
	feed := make(chan error, 1)
	go func() {
		_, e := io.Copy(in, src)
		if closeErr := in.Close(); e == nil {
			e = closeErr
		}
		feed <- e
	}()
	_, copyErr := io.Copy(dst, out)
	if copyErr != nil {
		_ = pigz.Process.Kill()
	}
	waitErr := pigz.Wait()
	if copyErr != nil || waitErr != nil {
		abortSource()
	}
	feedErr := <-feed
	switch {
	case copyErr != nil:
		return fmt.Errorf("stream mysqldump output: %w", copyErr)
	case waitErr != nil:
		return fmt.Errorf("pigz failed: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	case feedErr != nil:
		return fmt.Errorf("stream mysqldump output: %w", feedErr)
	}
	return nil
}
