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
	"regexp"
	"strings"
	"text/template"
	"time"
)

type Stats struct {
	Runs      int
	Successes int
}

var (
	iprouteOutputfile = "/tmp/ip_route_output.txt"
	ipLinkOutputFile  = "/tmp/ip_link_output.txt"
)

func main() {

	// Define flags
	promptFilePath := flag.String("promptfile", "prompts.txt", "Output just the CNI json instead of a net-attach-def")
	ollamaHost := flag.String("host", "", "The IP address of the ollama host")
	ollamaPort := flag.String("port", "11434", "The port address of the ollama service")
	ollamaModel := flag.String("model", "llama2:13b", "The port address of the ollama service")
	numberOfRuns := flag.Int("runs", 1, "Number of runs to run")
	introspectNetwork := flag.Bool("introspect", false, "Introspect networking on a k8s worker node")
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

	// Network introspection
	if *introspectNetwork {
		// kubectl get nodes --no-headers | grep -m 1 -v control-plane | awk '{print $1}'
		err := introspectNodeNetwork()
		if err != nil {
			fmt.Println("Error introspecting node network: ", err)
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
			_ = kubectlDelete(lastnetattachdef)
			lastnetattachdef = ""
		}

		fmt.Printf("------------------ RUN # %v\n", i)

		netattachdefstr, usedlinenumber, err := runRobocni(*promptFilePath, *ollamaHost, *ollamaPort, *ollamaModel, *introspectNetwork)
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
		_ = kubectlDelete(netattachdefstr)
		// Then create.
		err = kubectlCreate(netattachdefstr)
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
		_ = kubectlDelete(renderedpod)
		err = kubectlCreate(renderedpod)
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

// introspectNodeNetwork retrieves the first worker node and runs introspection commands
func introspectNodeNetwork() error {
	var nodename string
	output, err := runKubectl("get nodes --no-headers")
	if err != nil {
		return err
	}

	// Chew up the lines and grab the first worker node.
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if line == "" || strings.Contains(line, "control-plane") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			nodename = fields[0]
			break
		}
	}

	if nodename == "" {
		return fmt.Errorf("no worker node found")
	}

	fmt.Printf("Launching debugger pod on node: %s\n", nodename)
	debugPodName, err := launchDebuggerPod(nodename)
	if err != nil {
		return fmt.Errorf("Error launching debugger pod: %v\n", err)
	}
	fmt.Printf("Debugger pod launched: %s\n", debugPodName)

	err = waitForPodReady(debugPodName, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("Error waiting 5 minutes for debugger pod to be ready: %v\n", err)
	}

	// Run and save 'ip a' output
	err = executeAndSaveOutput(debugPodName, "chroot /host ip route", iprouteOutputfile)
	if err != nil {
		return fmt.Errorf("Error saving 'ip route' output: %v", err)
	}

	// Run and save 'ip link show' output
	err = executeAndSaveOutput(debugPodName, "chroot /host ip link show", ipLinkOutputFile)
	if err != nil {
		return fmt.Errorf("Error saving 'ip link show' output: %v", err)
	}

	fmt.Printf("Network introspection data saved to %s and %s\n", iprouteOutputfile, ipLinkOutputFile)
	return nil
}

// executeAndSaveOutput runs a command in the debugger pod and saves the output to a file
func executeAndSaveOutput(podName, command, outputFile string) error {
	cmd := exec.Command("kubectl", "exec", "-it", podName, "--", "bash", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to run command in pod %s: %v\nOutput: %s", podName, err, string(output))
	}

	// Write output to the specified file
	err = ioutil.WriteFile(outputFile, output, 0644)
	if err != nil {
		return fmt.Errorf("failed to write output to file %s: %v", outputFile, err)
	}
	return nil
}

// Launches the debugger pod on a specified node and returns the debugger pod's name
func launchDebuggerPod(nodeName string) (string, error) {
	cmd := exec.Command("kubectl", "debug", fmt.Sprintf("node/%s", nodeName), "--image=fedora", "--", "sleep", "500")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to launch debugger pod: %v\nOutput: %s", err, string(output))
	}

	// Extract the debugger pod name using regex
	re := regexp.MustCompile(`node-debugger-\S+`)
	matches := re.FindStringSubmatch(string(output))
	if len(matches) == 0 {
		return "", fmt.Errorf("could not find debugger pod name in output")
	}

	return matches[0], nil
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

func kubectlDelete(filecontents string) error {
	return kubectlResourceHandler(filecontents, true)
}

func kubectlCreate(filecontents string) error {
	return kubectlResourceHandler(filecontents, false)
}

func kubectlResourceHandler(filecontents string, delete bool) error {
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

func runRobocni(filePath string, llmHost string, llmPort string, llmModel string, introspect bool) (string, int, error) {
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

	var cmd *exec.Cmd
	// Create the command with flags, depending on if we're introspecting.
	if introspect {
		cmd = exec.Command(
			"robocni",
			"-host", llmHost,
			"-model", llmModel,
			"-port", llmPort,
			"-routefile", iprouteOutputfile,
			"-linkfile", ipLinkOutputFile,
			randomLine, // Add user hint as an argument
		)
	} else {
		cmd = exec.Command(
			"robocni",
			"-host", llmHost,
			"-model", llmModel,
			"-port", llmPort,
			randomLine, // Add user hint as an argument
		)
	}
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+llmHost)

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		fmt.Println("Command: robocni -host", llmHost, "-model", llmModel, "-port", llmPort, randomLine)
		fmt.Println("Command stderr:", stderr.String())
		fmt.Println("Command stdout:", out.String())
		return "", -1, fmt.Errorf("robocni command failed: %w", err)
	}

	return out.String(), usedlinenumber, nil
}
