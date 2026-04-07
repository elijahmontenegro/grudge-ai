package sandbox

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Sandbox manages Docker container lifecycle for tool execution.
// Fail fast: Docker unavailable = error to user, no tool execution.
type Sandbox struct {
	image       string
	workingDirs []string
}

// New creates a sandbox for the given working directories.
func New(workingDirs []string) *Sandbox {
	return &Sandbox{
		image:       "spidey-sandbox:latest",
		workingDirs: workingDirs,
	}
}

// Available checks if Docker is accessible.
func Available() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker unavailable: %w", err)
	}
	return nil
}

// Exec runs a command inside the Docker sandbox.
func (s *Sandbox) Exec(ctx context.Context, command string) (string, error) {
	args := []string{"run", "--rm", "-i"}

	// Mount working directories as volumes
	for _, dir := range s.workingDirs {
		args = append(args, "-v", dir+":"+dir)
	}

	if len(s.workingDirs) > 0 {
		args = append(args, "-w", s.workingDirs[0])
	}

	args = append(args, s.image, "sh", "-c", command)

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("sandbox exec: %w\n%s", err, output)
	}
	return string(output), nil
}

// ExecStream runs a command and streams output.
func (s *Sandbox) ExecStream(ctx context.Context, command string) (io.ReadCloser, error) {
	args := []string{"run", "--rm", "-i"}

	for _, dir := range s.workingDirs {
		args = append(args, "-v", dir+":"+dir)
	}

	if len(s.workingDirs) > 0 {
		args = append(args, "-w", s.workingDirs[0])
	}

	args = append(args, s.image, "sh", "-c", command)

	cmd := exec.CommandContext(ctx, "docker", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox start: %w", err)
	}

	return &streamReader{cmd: cmd, reader: stdout}, nil
}

type streamReader struct {
	cmd    *exec.Cmd
	reader io.ReadCloser
}

func (r *streamReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *streamReader) Close() error {
	r.reader.Close()
	return r.cmd.Wait()
}

// DetectDestructive checks if a bash command is destructive (rm, git push, etc.).
func DetectDestructive(command string) bool {
	destructive := []string{"rm ", "rm\t", "git push", "git reset --hard", "git checkout --"}
	lower := strings.ToLower(strings.TrimSpace(command))
	for _, d := range destructive {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}
