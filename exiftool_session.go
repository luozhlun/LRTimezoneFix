package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type exifToolCommandRunner interface {
	RunFiles(dir string, names []string, args ...string) ([]byte, string, error)
}

type directExifToolRunner struct {
	exifTool string
}

func (r directExifToolRunner) RunFiles(dir string, names []string, args ...string) ([]byte, string, error) {
	return runExifToolForFiles(r.exifTool, dir, names, args...)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) reset() {
	b.mu.Lock()
	b.buf.Reset()
	b.mu.Unlock()
}

func (b *lockedBuffer) string() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type exifToolSession struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	writer   *bufio.Writer
	reader   *bufio.Reader
	stderr   lockedBuffer
	nextID   uint64
	closed   bool
	waitDone chan error
}

func newExifToolSession(exifTool string) (*exifToolSession, error) {
	cmd := exec.Command(exifTool, "-stay_open", "True", "-@", "-")
	cmd.Env = cleanLocaleEnvironment(cmd.Environ())
	configureChildProcess(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}

	session := &exifToolSession{
		cmd:      cmd,
		stdin:    stdin,
		writer:   bufio.NewWriterSize(stdin, 64*1024),
		reader:   bufio.NewReaderSize(stdout, 256*1024),
		waitDone: make(chan error, 1),
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	go func() {
		_, _ = io.Copy(&session.stderr, stderr)
	}()
	go func() {
		session.waitDone <- cmd.Wait()
	}()
	return session, nil
}

func (s *exifToolSession) RunFiles(dir string, names []string, args ...string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, "", errors.New("ExifTool 常驻进程已经关闭")
	}

	s.nextID++
	id := strconv.FormatUint(s.nextID, 10)
	readyMarker := "{ready" + id + "}"
	s.stderr.reset()

	commandArgs := make([]string, 0, len(args)+len(names)+4)
	commandArgs = append(commandArgs, args...)
	commandArgs = append(commandArgs,
		"-charset", "filename=UTF8",
	)
	for _, name := range names {
		path := name
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		commandArgs = append(commandArgs, filepath.Clean(path))
	}

	for _, arg := range commandArgs {
		if strings.ContainsAny(arg, "\r\n") {
			return nil, "", fmt.Errorf("ExifTool 常驻参数包含换行符，无法安全传递")
		}
		if _, err := s.writer.WriteString(arg + "\n"); err != nil {
			return nil, s.stderr.string(), err
		}
	}
	if _, err := s.writer.WriteString("-execute" + id + "\n"); err != nil {
		return nil, s.stderr.string(), err
	}
	if err := s.writer.Flush(); err != nil {
		return nil, s.stderr.string(), err
	}

	var output bytes.Buffer
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return output.Bytes(), s.stderr.string(), fmt.Errorf("ExifTool 常驻进程意外结束：%w", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == readyMarker:
			return output.Bytes(), s.stderr.string(), nil
		default:
			output.WriteString(line)
		}
	}
}

func (s *exifToolSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	_, writeErr := s.writer.WriteString("-stay_open\nFalse\n")
	flushErr := s.writer.Flush()
	_ = s.stdin.Close()
	s.mu.Unlock()

	waitErr := <-s.waitDone
	if writeErr != nil {
		return writeErr
	}
	if flushErr != nil {
		return flushErr
	}
	return waitErr
}
