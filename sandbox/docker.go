// Package sandbox runs agent Bash tool calls inside a Docker container
// instead of on the host. Activated per-thread via Thread.Sandboxed.
//
// Threat model: protect the user from accidental destructive commands the
// model emits (rm -rf, overwrites outside the project) by fencing the
// filesystem to the bind-mounted working directories. This is NOT a
// security boundary against hostile code — the image shares the host
// kernel. See SANDBOX.md for details.
//
// Runtime contract: Docker CLI is invoked directly (no SDK) to keep the
// dependency surface minimal. The image is built from
// containers/sandbox/Dockerfile via `docker compose build sandbox`.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
)

// Image is the tag of the sandbox image the Grudge service expects.
// Matches the `image:` field of the `sandbox` service in docker-compose.yml
// and the tag produced by `docker compose build sandbox`.
const Image = "grudge-sandbox:latest"

// BuildCommand is the one-liner users run to produce the image. Embedded
// in error messages so remediation is always one copy-paste away.
const BuildCommand = "docker compose build sandbox"

// Sandbox manages Docker container lifecycle for tool execution.
//
// A sandbox binds exactly one host directory — the thread's workspace —
// into the container at ContainerWorkspace (/workspace). The agent's
// file tools (FileRead/FileWrite/FileEdit/NotebookEdit) stay on the
// host but path-guard every access against the same workspace root, so
// both sides see the same filesystem. That shared view is what lets
// the model reason coherently about what it wrote, what it can read,
// and what Bash will find.
//
// No other host directories are exposed: no home, no repo, no project
// root. Prompt-injected paths to host secrets simply cannot resolve.
type Sandbox struct {
	image     string
	workspace string // absolute host path bind-mounted at /workspace
}

// New creates a sandbox bound to the given workspace directory.
// The directory must already exist and be absolute (callers should
// use WorkspaceDir to get one).
func New(workspace string) *Sandbox {
	return &Sandbox{
		image:     Image,
		workspace: workspace,
	}
}

// ErrDockerUnavailable is returned when the Docker daemon isn't reachable.
// Separated from ErrImageMissing so callers can give different remediation
// ("start Docker Desktop" vs "build the image").
var ErrDockerUnavailable = errors.New("docker daemon unavailable — start Docker Desktop or the docker service")

// ErrImageMissing is returned when the daemon is up but grudge-sandbox is
// not present locally. Build it via the command in BuildCommand.
var ErrImageMissing = fmt.Errorf("sandbox image %s not found locally — run `%s`", Image, BuildCommand)

// CheckReady verifies that the Docker daemon is reachable and that the
// sandbox image exists locally. Intended for startup preflight and for
// the "am I ready?" check the UI can surface.
//
// Fail-fast: we do NOT attempt to pull the image — there is no public
// registry for grudge-sandbox and a pull would produce a confusing
// "pull access denied" error. The user must build it.
func CheckReady() error {
	if err := dockerAvailable(); err != nil {
		return err
	}
	return imageExists(Image)
}

func dockerAvailable() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnavailable, err)
	}
	return nil
}

func imageExists(tag string) error {
	cmd := exec.Command("docker", "image", "inspect", tag)
	if err := cmd.Run(); err != nil {
		return ErrImageMissing
	}
	return nil
}

// Exec runs a command inside a fresh container. Stdout+stderr are
// captured together; the caller decides how to surface them.
//
// Preflight: CheckReady is called once per invocation. It's cheap
// (two local Docker CLI calls) and guarantees the error returned to
// the agent is actionable rather than a raw docker pull failure.
func (s *Sandbox) Exec(ctx context.Context, command string) (string, error) {
	if err := CheckReady(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "docker", s.runArgs(command)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("sandbox exec: %w\n%s", err, output)
	}
	return string(output), nil
}

// ExecStream starts a command and returns a reader for its combined
// output. The caller MUST Close the returned ReadCloser to reap the
// container and release the goroutine.
func (s *Sandbox) ExecStream(ctx context.Context, command string) (io.ReadCloser, error) {
	if err := CheckReady(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", s.runArgs(command)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sandbox start: %w", err)
	}
	return &streamReader{cmd: cmd, reader: stdout}, nil
}

// runArgs builds the `docker run` argv for this sandbox. Shared by
// Exec and ExecStream so the invocation shape stays in one place.
//
// Flags in use:
//
//	--rm                         container is one-shot
//	-i                           keep stdin open for future stdin piping
//	-v {host}:{ContainerWorkspace}  bind-mount workspace at /workspace
//	-w {ContainerWorkspace}      initial cwd is the workspace
//	<image> sh -c                shell for pipes, &&, $VAR
func (s *Sandbox) runArgs(command string) []string {
	args := []string{"run", "--rm", "-i"}
	if s.workspace != "" {
		args = append(args, "-v", s.workspace+":"+ContainerWorkspace, "-w", ContainerWorkspace)
	}
	return append(args, s.image, "sh", "-c", command)
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

// destructivePatterns matches a command *invocation* (not arbitrary
// text). Each pattern anchors on start-of-line or pipeline-separator
// so novel prose containing "arm " or "form " inside a quoted heredoc
// doesn't trip the detector. Case-insensitive via (?i) on the caller.
//
// The patterns cover the same commands as before:
//   - rm (with space/tab — catches rm -rf, rm /path)
//   - git push
//   - git reset --hard
//   - git checkout --
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?mi)(?:^|[;&|])\s*(?:sudo\s+)?rm\b`),
	regexp.MustCompile(`(?mi)(?:^|[;&|])\s*git\s+push\b`),
	regexp.MustCompile(`(?mi)(?:^|[;&|])\s*git\s+reset\s+--hard\b`),
	regexp.MustCompile(`(?mi)(?:^|[;&|])\s*git\s+checkout\s+--`),
}

// DetectDestructive identifies commands whose top-level invocation is
// rm, git push, git reset --hard, or git checkout --. Used by callers
// running commands on the HOST (sandboxed=false) as a belt-and-suspenders
// gate. Sandboxed threads don't need it — the container boundary scopes
// destruction to the workspace.
//
// The matcher only inspects command positions, not arbitrary text. A
// heredoc whose content happens to include the substring "rm " will not
// trigger; a command like `rm -rf /workspace/foo` will.
func DetectDestructive(command string) bool {
	for _, re := range destructivePatterns {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}
