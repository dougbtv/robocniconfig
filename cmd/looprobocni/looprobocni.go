package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

type Stats struct {
	Runs      int
	Successes int
}

func main() {

	// Define flags
	promptFilePath := flag.String("promptfile", "prompts.txt", "Output just the CNI json instead of a net-attach-def")
	ollamaHost := flag.String("host", "", "The IP address of the ollama host")
	numberOfRuns := flag.Int("runs", 1, "Number of runs to run")
	help := flag.Bool("help", false, "Display help information")

	// Parse the flags
	flag.Parse()

	// Check if help was requested
	if *help {
		fmt.Println("Usage of robocni:")
		flag.PrintDefaults() // This will print out all defined flags
		os.Exit(0)
	}

	if *ollamaHost == "" {
		*ollamaHost = os.Getenv("OLLAMA_HOST")
		if *ollamaHost == "" {
			fmt.Println("Please set --host or the OLLAMA_HOST environment variable.")
			os.Exit(1)
		}
	}

	var numerrors int
	var numgenerationerrors int
	var failedpodcreate int
	var pingerrors int
	var totalruns int
	lastnetattachdef := ""
	numhintlines, err := countLinesofHint(*promptFilePath)
	if err != nil {
		fmt.Println("Could not open prompt file: " + *promptFilePath + "  make sure to set --promptfile or name it prompts.txt")
	}
	statsArray := make([]Stats, numhintlines)

	for i := 1; i <= *numberOfRuns; i++ {

		totalruns++

		if i > 1 {
			generateReport(i-1, numerrors, numgenerationerrors, failedpodcreate, pingerrors, statsArray)
		}

		// Delete the last netattachdef.
		if lastnetattachdef != "" {
			_ = kubectlCreate(lastnetattachdef, true)
			lastnetattachdef = ""
		}

		fmt.Printf("------------------ RUN # %v\n", i)

		netattachdefstr, usedlinenumber, err := runRobocni(*promptFilePath, *ollamaHost)
		if err != nil {
			fmt.Printf("Error generating robocni net-attach-def, run #%d: %v\n", i, err)
			numerrors++
			numgenerationerrors++
			continue
		}

		statsArray[usedlinenumber].Runs++

		fmt.Printf("---\n%s\n", netattachdefstr)
		// fmt.Printf("Run %d usedline: %v\n", i+1, usedlinenumber)

		parsedname, err := parseName(netattachdefstr)
		// Prefix the name so we can find all these.
		if err != nil {
			fmt.Printf("Error parsing name: %s\n", err)
			numerrors++
			continue
		}
		fmt.Println("Parsed name: " + parsedname)

		// Delete for redundancy in case.
		_ = kubectlCreate(netattachdefstr, true)
		// Then create.
		err = kubectlCreate(netattachdefstr, false)
		if err != nil {
			fmt.Printf("Error doing kubectl create for net attach def: %s\n", err)
			numerrors++
			continue
		}
		lastnetattachdef = netattachdefstr

		// Testing the method...
		// podresult, err := runKubectl("get pods -o wide")
		// if err != nil {
		// 	fmt.Printf("Kubectl failed for get pods: %v", err)
		// }
		// fmt.Println("pod result: " + podresult)

		poddata := PodTemplateData{
			NetAttachDefName: parsedname,
		}

		renderedpod := templatePod(poddata)
		// fmt.Println("rendered pod: \n" + renderedpod)

		fmt.Println("Spinning up pods...")

		// Delete (with confidence) then create pod.
		_ = kubectlCreate(renderedpod, true)
		err = kubectlCreate(renderedpod, false)
		if err != nil {
			fmt.Printf("Error doing kubectl create for pods: %s\n", err)
			numerrors++
			failedpodcreate++
			continue
		}

		// Wait for the pod to be ready with a 1-minute timeout
		if err := waitForPodReady("testpod-left", 30*time.Second); err != nil {
			fmt.Printf("Error waiting for pod kubectl create (left): %s\n", err)
			numerrors++
			failedpodcreate++
			continue
		} else {
			fmt.Println("Pod left is ready")
		}

		// And now just give 10 seconds for the other side
		if err := waitForPodReady("testpod-right", 15*time.Second); err != nil {
			fmt.Printf("Error waiting for pod kubectl create (right): %s\n", err)
			numerrors++
			failedpodcreate++
			continue
		} else {
			fmt.Println("Pod right is ready")
		}

		ip, err := getIPForNet1("testpod-right")
		if err != nil {
			fmt.Println("Error getting IP address:", err)
			numerrors++
			failedpodcreate++
			continue
		}

		fmt.Println("IP Address for net1:", ip)

		pingcmd := fmt.Sprintf("exec -i testpod-left -- ping -i 0.5 -c3 %s", ip)
		// fmt.Println("Ping command: ", pingcmd)
		_, err = runKubectl(pingcmd)
		if err != nil {
			fmt.Printf("Failed to ping %s", ip)
			numerrors++
			pingerrors++
			continue
		}

		// fmt.Println("Ping output: " + pingoutput)
		statsArray[usedlinenumber].Successes++

	}

	generateReport(totalruns, numerrors, numgenerationerrors, failedpodcreate, pingerrors, statsArray)

}

func generateReport(runNumber, numErrors, numGenerationErrors, failedPodCreate, pingErrors int, statsArray []Stats) {
	fmt.Printf("---\n")
	fmt.Printf("Run number: %d\n", runNumber)
	fmt.Printf("Total Errors: %d (%.2f%%)\n", numErrors, percent(numErrors, runNumber))
	fmt.Printf("Generation Errors: %d (%.2f%%)\n", numGenerationErrors, percent(numGenerationErrors, runNumber))
	fmt.Printf("Failed Pod Creations: %d (%.2f%%)\n", failedPodCreate, percent(failedPodCreate, runNumber))
	fmt.Printf("Ping Errors: %d (%.2f%%)\n", pingErrors, percent(pingErrors, runNumber))

	fmt.Println("Stats Array:")
	for j, stat := range statsArray {
		fmt.Printf("  Hint %d: Runs: %d, Successes: %d\n", j+1, stat.Runs, stat.Successes)
	}
}

func percent(count, total int) float64 {
	if total == 0 {
		return 0
	}
	return (float64(count) / float64(total)) * 100
}

func getIPForNet1(podName string) (string, error) {
	// Retrieve network-status annotation
	command := fmt.Sprintf("get pod %s -o jsonpath={.metadata.annotations.k8s\\.v1\\.cni\\.cncf\\.io/network-status}", podName)
	// fmt.Println("get pod command: " + command)
	annotation, err := runKubectl(command)
	if err != nil {
		return "", err
	}
	// fmt.Println("annotation found: " + annotation)

	// Parse the JSON in the annotation
	var networkStatuses []struct {
		Interface string   `json:"Interface"`
		IPs       []string `json:"ips"`
	}
	err = json.Unmarshal([]byte(annotation), &networkStatuses)
	if err != nil {
		return "", fmt.Errorf("failed to parse network-status JSON: %v", err)
	}

	// Find and return the first IP for "net1"
	for _, status := range networkStatuses {
		// fmt.Printf("each Network status: %+v", status)
		if status.Interface == "net1" {
			if len(status.IPs) > 0 {
				return status.IPs[0], nil
			}
			break
		}
	}

	return "", fmt.Errorf("no IP found for net1")
}

func waitForPodReady(podName string, timeout time.Duration) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	timeoutChan := time.After(timeout)

	for {
		select {
		case <-ticker.C:
			if isPodReady(podName) {
				return nil
			}
		case <-timeoutChan:
			return fmt.Errorf("timeout waiting for pod '%s' to be ready", podName)
		}
	}
}

func isPodReady(podName string) bool {
	cmd := exec.Command("kubectl", "get", "pod", podName, "-o", "jsonpath={.status.phase}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Error getting pod status: %v\n", err)
		return false
	}
	return strings.TrimSpace(string(output)) == "Running"
}

//go:embed templates/pod.yml
var podtemplateBlob embed.FS

// Template Structs
type PodTemplateData struct {
	NetAttachDefName string
}

func templatePod(data PodTemplateData) string {
	// Read the embedded template file
	tmpl, err := podtemplateBlob.ReadFile("templates/pod.yml")
	if err != nil {
		panic(err)
	}

	// Parse the template
	t, err := template.New("template").Parse(string(tmpl))
	if err != nil {
		panic(err)
	}

	// Execute the template with the data
	var tpl bytes.Buffer
	err = t.Execute(&tpl, data)
	if err != nil {
		panic(err)
	}

	// Print the result
	// logErr(tpl.String())
	return tpl.String()
}

func kubectlCreate(filecontents string, delete bool) error {
	// Create a temp file
	tmpFile, err := ioutil.TempFile("", "example.*.yml")
	if err != nil {
		return fmt.Errorf("Failed to create temp file: %v\n", err)
	}
	defer os.Remove(tmpFile.Name()) // Clean up the file afterwards

	// Write data to temp file
	if _, err := tmpFile.WriteString(filecontents); err != nil {
		return fmt.Errorf("Failed to write to temp file: %v\n", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("Failed to close temp file: %v\n", err)
	}

	// Run kubectl command with temp file
	verb := "create"
	if delete {
		verb = "delete"
	}
	kubectlCmd := fmt.Sprintf(verb+" -f %s", filepath.ToSlash(tmpFile.Name()))
	_, err = runKubectl(kubectlCmd)
	if err != nil {
		return fmt.Errorf("Error running kubectl command: %v\n", err)
	}

	return nil
}

// runKubectl runs a kubectl command with the provided command line and returns combined output
func runKubectl(commandLine string) (string, error) {
	// Split the command line into command and arguments
	args := strings.Fields(commandLine)

	// Create the command
	cmd := exec.Command("kubectl", args...)

	// Get combined stdout and stderr
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command 'kubectl %s' failed: %v\nOutput: %s", commandLine, err, string(output))
	}

	return string(output), nil
}

func parseName(blob string) (string, error) {
	// Find the start index of "metadata:"
	startIndex := strings.Index(blob, "metadata:")
	if startIndex == -1 {
		return "", fmt.Errorf("metadata section not found")
	}

	// Find the start index of "name:"
	nameIndex := strings.Index(blob[startIndex:], "name:")
	if nameIndex == -1 {
		return "", fmt.Errorf("name field not found")
	}

	// Adjust nameIndex to start from the beginning of the blob
	nameIndex += startIndex

	// Find the end index (assuming it ends with a newline character)
	endIndex := strings.Index(blob[nameIndex:], "\n")
	if endIndex == -1 {
		return "", fmt.Errorf("end of name field not found")
	}

	// Adjust endIndex to start from the beginning of the blob
	endIndex += nameIndex

	// Extract the name value
	nameLine := blob[nameIndex:endIndex]
	parts := strings.Split(nameLine, ":")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid format for name field")
	}

	// Trim spaces and return the name
	return strings.TrimSpace(parts[1]), nil
}

func countLinesofHint(filePath string) (int, error) {
	// Read the file
	fileContent, err := ioutil.ReadFile(filePath)
	if err != nil {
		return 0, fmt.Errorf("Error reading file: %v\n", err)
	}

	// Split the file content into lines and count them
	lines := strings.Split(string(fileContent), "\n")
	numLines := len(lines)

	return numLines, nil
}

func runRobocni(filePath string, llmHost string) (string, int, error) {
	// Replace with your file path

	// Read the file
	fileContent, err := ioutil.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading prompts file @ %v: %v\n", filePath, err)
		return "", -1, err
	}

	// Split the file content into lines
	lines := strings.Split(strings.TrimSpace(string(fileContent)), "\n")

	// Seed the random number generator
	rand.Seed(time.Now().UnixNano())

	// Select a random line
	usedlinenumber := rand.Intn(len(lines))
	randomLine := lines[usedlinenumber]

	fmt.Println("User hint: ", randomLine)
	// Create the command
	// Replace 'yourRobocniBinaryPath' with the path to your robocni binary
	cmd := exec.Command("robocni", randomLine)
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+llmHost)

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		fmt.Println("Command: robotcni " + randomLine)
		fmt.Println("Command stderr:", stderr.String())
		fmt.Println("Command stdout:", out.String())
		return "", -1, fmt.Errorf("robocni command failed: %w", err)
	}

	return out.String(), usedlinenumber, nil
}
