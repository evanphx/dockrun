package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type cmdResult struct {
	output   string
	exitCode int
	err      error
}

func getExitCode(err error) (int, error) {
	exitCode := 0
	if exiterr, ok := err.(*exec.ExitError); ok {
		if procExit := exiterr.Sys().(syscall.WaitStatus); ok {
			return procExit.ExitStatus(), nil
		}
	}
	return exitCode, fmt.Errorf("failed to get exit code")
}

func runCommandWithOutput(cmd *exec.Cmd) (output string, exitCode int, err error) {
	exitCode = 0
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exiterr error
		if exitCode, exiterr = getExitCode(err); exiterr != nil {
			// TODO: Fix this so we check the error's text.
			// we've failed to retrieve exit code, so we set it to 127
			exitCode = 127
		}
	}
	output = string(out)
	return
}

func runCommandWithOutputResult(cmd *exec.Cmd) cmdResult {
	output, exitCode, err := runCommandWithOutput(cmd)
	return cmdResult{output, exitCode, err}
}

func runCommandSendResult(cmd *exec.Cmd, c chan cmdResult) {
	c <- runCommandWithOutputResult(cmd)
}

func waitForResult(containerID string, signals chan os.Signal, waitCmd chan cmdResult) cmdResult {
	var action string
	for {
		select {
		case sig := <-signals:
			switch sig {
			case os.Interrupt:
				action = "stop"
			case os.Kill:
				action = "kill"
			}
			fmt.Printf("Received signal: %s; cleaning up\n", sig)
			cmd := exec.Command("docker", action, containerID)
			out, _, err := runCommandWithOutput(cmd)
			if err != nil || strings.Contains(out, "Error") {
				fmt.Printf("stopping container via signal %s failed\n", sig)
			}
		case waitResult := <-waitCmd:
			return waitResult
		}
	}
}

func validateArgs(args []string) {
	failed := false
	if len(args) < 1 {
		fmt.Println("dockrun [OPTIONS] IMAGE [COMMAND]\n")
		fmt.Println("OPTIONS - same options as docker run, without -a & -i")
		failed = true
	}

	for _, val := range args {
		if val == "-i" {
			fmt.Println("ERROR: dockrun doesn't support -i")
			failed = true
		}
		if val == "-a" {
			fmt.Printf("ERROR: dockrun doesn't support -a")
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

// WARNING: 'docker wait', 'docker logs', 'docker rm', 'docker kill' and 'docker stop'
// exit with status code 0 even if they've failed.

func main() {
	var containerID string
	var finalExitCode int
	defaultArgs := []string{"run", "-d"}

	args := os.Args[1:]
	validateArgs(args)
	finalArgs := append(defaultArgs, args...)

	runCmd := exec.Command("docker", finalArgs...)
	if out, exitCode, err := runCommandWithOutput(runCmd); err != nil {
		fmt.Printf("docker run: %s", out)
		fmt.Printf("ERROR docker exited with exit code: %d\n", exitCode)
		os.Exit(1)
	} else {
		containerID = strings.Trim(out, "\n")
	}
	if len(containerID) < 4 {
		fmt.Printf("ERROR: docker container ID is too small, possibly invalid")
		os.Exit(1)
	}

	// hack to handle signals & wait for "docker wait" to be finished
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, os.Kill)
	waitCmdRes := make(chan cmdResult, 1)
	waitCmd := exec.Command("docker", "wait", containerID)
	go runCommandSendResult(waitCmd, waitCmdRes)
	waitResult := waitForResult(containerID, signals, waitCmdRes)

	waitOutput := waitResult.output
	waiterr := waitResult.err
	// try to run 'docker wait' again; this is needed when we receive a
	// signal and 'docker wait' fails to retrieve the correct exit code
	// of the container
	if waiterr != nil {
		waitCmd := exec.Command("docker", "wait", containerID)
		waitOutput, _, waiterr = runCommandWithOutput(waitCmd)
	}
	// end hack

	if waiterr != nil || strings.Contains(waitOutput, "Error") {
		// docker wait failed
		fmt.Printf("ERROR: docker wait: %s %s\n", waitOutput, waiterr)
		fmt.Printf("ERROR: docker wait failed\n")
		os.Exit(1)
	}
	waitOutput = strings.Trim(waitOutput, "\n")
	finalExitCode, err := strconv.Atoi(waitOutput)
	if err != nil {
		fmt.Println(waitOutput)
		fmt.Printf("ERROR: failed to convert exit code to int\n")
		os.Exit(1)
	}

	logsCmd := exec.Command("docker", "logs", containerID)
	logsOutput, _, logserr := runCommandWithOutput(logsCmd)
	if logserr != nil || strings.Contains(logsOutput, "No such container") {
		fmt.Printf("ERROR: docker logs: %s %s\n", logsOutput, logserr)
		fmt.Printf("ERROR: docker logs failed\n")
	} else {
		fmt.Printf(logsOutput)
	}

	rmCmd := exec.Command("docker", "rm", containerID)
	rmOutput, _, rmerr := runCommandWithOutput(rmCmd)
	if rmerr != nil || strings.Contains(rmOutput, "Error") {
		fmt.Printf("ERROR: docker rm: %s %s\n", rmOutput, rmerr)
		fmt.Printf("ERROR: docker rm failed\n")
		// fall through and let the return code of the container go through
	}
	os.Exit(finalExitCode)
}