package engine

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf8"
)

// Limits for the command output a failed script or command gate hands to the
// node output and to the upstream rework feedback. Test runners print the
// failure summary last, so the tail is what the next attempt needs.
const (
	commandFailureTailLines = 80
	commandFailureTailBytes = 4000
	commandFailureCmdBytes  = 500
)

// commandFailure carries the command, exit code and output tail of a failed
// script node or command gate. Error() stays the bare cause: the engine
// classifies failures by substring (sandbox interrupts, max turns,
// "verification "), and command output must not be able to steer that, e.g. a
// test log printing "signal: killed" would otherwise requeue the node forever.
// failureFeedback appends the detail wherever a person or the next attempt
// reads the failure.
type commandFailure struct {
	err    error
	detail string
}

func (f *commandFailure) Error() string { return f.err.Error() }
func (f *commandFailure) Unwrap() error { return f.err }

func withCommandOutput(err error, command, output string) error {
	if err == nil {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "命令：\n%s\n", clipCommand(strings.TrimSpace(command)))
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		fmt.Fprintf(&b, "退出码：%d\n", exitErr.ExitCode())
	}
	tail := outputTail(output, commandFailureTailLines, commandFailureTailBytes)
	if tail == "" {
		tail = "（无输出）"
	}
	b.WriteString("输出尾部：\n" + tail)
	return &commandFailure{err: err, detail: b.String()}
}

// failureFeedback is the failure text for people and for the next attempt:
// the error plus the command output when the failure carries it.
func failureFeedback(err error) string {
	if err == nil {
		return ""
	}
	var f *commandFailure
	if errors.As(err, &f) {
		return err.Error() + "\n\n" + f.detail
	}
	return err.Error()
}

func commandFailureDetail(err error) (string, bool) {
	var f *commandFailure
	if errors.As(err, &f) {
		return f.detail, true
	}
	return "", false
}

func clipCommand(command string) string {
	if len(command) <= commandFailureCmdBytes {
		return command
	}
	cut := commandFailureCmdBytes
	for cut > 0 && !utf8.RuneStart(command[cut]) {
		cut--
	}
	return command[:cut] + "…"
}

// outputTail keeps the last maxLines lines and at most maxBytes, cut on a
// rune boundary, and marks the cut.
func outputTail(output string, maxLines, maxBytes int) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	cut := false
	lines := strings.Split(output, "\n")
	if len(lines) > maxLines {
		lines, cut = lines[len(lines)-maxLines:], true
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > maxBytes {
		start := len(tail) - maxBytes
		for start < len(tail) && !utf8.RuneStart(tail[start]) {
			start++
		}
		tail, cut = tail[start:], true
	}
	if cut {
		return "…（前面的输出已省略）\n" + tail
	}
	return tail
}
