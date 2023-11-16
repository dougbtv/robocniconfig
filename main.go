package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// Define a struct to unmarshal the JSON response
type LLMResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
}

// Embed the file
//
//go:embed base_query.txt
var basequeryBlob embed.FS

// Define a struct for template data
type QueryTemplateData struct {
	Interfaces string
	Routes     string
	Hint       string
}

func main() {

	// Define flags
	dryRun := flag.Bool("dry-run", false, "Perform a dry run without making any changes")
	ollamaHost := flag.String("host", "", "The IP address of the ollama host")
	ollamaPort := flag.String("port", "11434", "The port address of the ollama service")
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

	// Introspect the Host
	fmt.Println("Listing Network Interfaces:")

	ifs, err := listNetworkInterfaces()
	if err != nil {
		panic(err)
	}

	routes, err := getIPRoute()
	if err != nil {
		panic(err)
	}

	data := QueryTemplateData{
		Interfaces: ifs,
		Routes:     routes,
	}

	var jsonStr string
	for i := 0; i < 5; i++ {

		// Step 2: Query the LLM
		query := templateQuery(data)
		response, err := queryLLM(*ollamaHost, *ollamaPort, query)
		if err != nil {
			fmt.Println(err)
		} else {
			fmt.Println("LLM Response:", response)
		}

		jsonStr, err = parseAndValidateJSON(response)
		if err != nil {
			fmt.Printf("Attempt %d failed: %v\n", i+1, err)
			continue
		}

		break

	}

	fmt.Println("Valid JSON:", jsonStr)

	if *dryRun {
		fmt.Println("Dry run:", jsonStr)
		os.Exit(0)
	}

}

func parseAndValidateJSON(response string) (string, error) {
	start := strings.Index(response, "```")
	end := strings.LastIndex(response, "```")

	if start == -1 || end == -1 || start == end {
		return "", errors.New("no valid backtick-enclosed text found")
	}

	// Extract text between backticks
	jsonStr := response[start+3 : end]

	// Check if the text is valid JSON
	var js json.RawMessage
	err := json.Unmarshal([]byte(jsonStr), &js)
	if err != nil {
		return "", fmt.Errorf("invalid JSON: %v", err)
	}

	return jsonStr, nil
}

func templateQuery(data QueryTemplateData) string {
	// Read the embedded template file
	tmpl, err := basequeryBlob.ReadFile("base_query.txt")
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
	fmt.Println(tpl.String())
	return tpl.String()
}

func listNetworkInterfaces() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		fmt.Println(err)
		return "", fmt.Errorf("error listing interfaces: %v", err)
	}

	var result string
	for _, intf := range interfaces {
		result += fmt.Sprintf("%v\n", intf.Name)
		// Add more details as needed
	}

	return result, nil
}

func getIPRoute() (string, error) {
	cmd := exec.Command("ip", "route")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

func queryLLM(ollamaHost string, ollamaPort string, query string) (string, error) {
	// Define the URL and payload
	url := "http://" + ollamaHost + ":" + ollamaPort + "/api/generate"
	payload := map[string]string{"model": "llama2:13b", "prompt": query}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("error marshalling payload: %v", err)
	}
	body := bytes.NewReader(payloadBytes)

	// Make the POST request
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return "", fmt.Errorf("error creating POST request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Perform the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error performing POST request: %v", err)
	}
	defer resp.Body.Close()

	// Read the response body
	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	// Split the response body into lines and process each line
	var finalResponse string
	lines := strings.Split(string(responseBody), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}

		var response LLMResponse
		err = json.Unmarshal([]byte(line), &response)
		if err != nil {
			return "", fmt.Errorf("error unmarshalling response JSON line: %v", err)
		}

		finalResponse += response.Response
	}

	return strings.TrimSpace(finalResponse), nil
}
