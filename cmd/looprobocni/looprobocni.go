package main

import (
	"bytes"
	"fmt"
	"os/exec"
)

func main() {
	const numberOfRuns = 5

	for i := 0; i < numberOfRuns; i++ {
		output, err := runRobocni()
		if err != nil {
			fmt.Printf("Error during run %d: %v\n", i+1, err)
			continue
		}

		fmt.Printf("Run %d output:\n%s\n", i+1, output)
		// Further processing can be done here with the output

		// For example, save the output to a file or parse it
		// to extract required information
	}
}

func runRobocni() (string, error) {
	// Replace 'yourRobocniBinaryPath' with the path to your robocni binary
	cmd := exec.Command("yourRobocniBinaryPath")

	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("robocni command failed: %w", err)
	}

	return out.String(), nil
}
