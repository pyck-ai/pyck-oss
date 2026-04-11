package commands

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
)

func RunCommandWithLogs(cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	go readAndPrintLogs(stdout)
	go readAndPrintLogs(stderr)

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}

func readAndPrintLogs(reader io.ReadCloser) {
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
	err := scanner.Err()
	if err != nil {
		fmt.Println("Error reading logs: ", err)
	}
}
