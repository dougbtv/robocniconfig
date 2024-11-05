package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"text/template"
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
//go:embed templates/base_query.txt
var basequeryBlob embed.FS

//go:embed templates/netattachdef_template.txt
var netattachdefBlob embed.FS

// Template Structs
type QueryTemplateData struct {
	Interfaces string
	Routes     string
	Hint       string
}

type NetAttachDefTemplateData struct {
	CNIConfig string
	CNIName   string
}

func main() {

	// Define flags
	useJsonOutput := flag.Bool("json", false, "Output just the CNI json instead of a net-attach-def")
	useDebug := flag.Bool("debug", false, "Show debug output, especially entire response from LLM")
	ollamaHost := flag.String("host", "", "The IP address of the ollama host")
	ollamaPort := flag.String("port", "11434", "The port address of the ollama service")
	ollamaModel := flag.String("model", "llama2:13b", "The port address of the ollama service")
	fileRoutes := flag.String("routefile", "", "File containing the output of 'ip route' command")
	fileIPAddress := flag.String("ipafile", "", "File containing the output of 'ip address' command")
	help := flag.Bool("help", false, "Display help information")

	// Parse the flags
	flag.Parse()

	// Check if help was requested
	if *help {
		logErr("Usage of robocni:")
		flag.PrintDefaults() // This will print out all defined flags
		os.Exit(0)
	}

	// Get non-flag arguments
	args := flag.Args()
	if len(args) == 0 {
		logErr("You must provide a 'hint' as the last parameter, for example run it like: './robocni \"Use bridge CNI and whereabouts CNI with 192.168.50.0/24 range\"'")
		os.Exit(1)
	}

	// The last positional argument
	userHint := args[len(args)-1]

	if *ollamaHost == "" {
		*ollamaHost = os.Getenv("OLLAMA_HOST")
		if *ollamaHost == "" {
			logErr("Please set --host or the OLLAMA_HOST environment variable.")
			os.Exit(1)
		}
	}

	// Introspect the Host
	// logErr("Listing Network Interfaces:")

	var ifs, routes string
	// Check and read the interface file if provided
	if *fileIPAddress != "" {
		if _, err := os.Stat(*fileIPAddress); os.IsNotExist(err) {
			fmt.Printf("Error: file %s does not exist\n", *fileIPAddress)
			os.Exit(1)
		} else {
			content, err := ioutil.ReadFile(*fileIPAddress)
			if err != nil {
				fmt.Printf("Error reading IP address file %s: %v\n", *fileIPAddress, err)
				os.Exit(1)
			}
			ifs = string(content)
		}
	}

	// Check and read the route file if provided
	if *fileRoutes != "" {
		if _, err := os.Stat(*fileRoutes); os.IsNotExist(err) {
			fmt.Printf("Error: file %s does not exist\n", *fileRoutes)
			os.Exit(1)
		} else {
			content, err := ioutil.ReadFile(*fileRoutes)
			if err != nil {
				fmt.Printf("Error reading routes file %s: %v\n", *fileRoutes, err)
				os.Exit(1)
			}
			routes = string(content)
		}
	}

	data := QueryTemplateData{
		Interfaces: ifs,
		Routes:     routes,
		Hint:       userHint,
	}
	var extractedjson, cniname string
	found := false
	for i := 0; i < 5; i++ {

		// Step 2: Query the LLM
		query := templateQuery(data)
		response, err := queryLLM(*ollamaHost, *ollamaPort, *ollamaModel, *useDebug, query)
		if err != nil {
			logErr(fmt.Sprintf("%v", err))
		} else {
			// logErr("!bang LLM Response:", response)
		}

		extractedjson, cniname, err = parseAndValidateJSON(response)
		if err != nil {
			logErr(fmt.Sprintf("Attempt %d/5 failed: %v", i+1, err))
			continue
		}

		found = true
		break

	}

	if !found {
		logErr("LLM Query failed in 5 tries :( #failburger")
		os.Exit(1)
	}

	// logErr(fmt.Sprintf("Generating net-attach-def for: %v", cniname))
	// logErr("Valid JSON:", extractedjson)

	netattachdefdata := NetAttachDefTemplateData{
		CNIName:   cniname,
		CNIConfig: strings.TrimSpace(extractedjson),
	}

	renderednetattachdef := templateNetAttachDef(netattachdefdata)

	// Just output the net-attach-def, or, JSON if requested
	if *useJsonOutput {
		fmt.Printf(extractedjson)
	} else {
		fmt.Printf(renderednetattachdef)
		os.Exit(0)
	}

}

func logErr(str string) {
	fmt.Fprintln(os.Stderr, str)
}

func parseAndValidateJSON(response string) (string, string, error) {
	// Find the start of the code block, supporting both ``` and ```json
	start := strings.Index(response, "```")
	if start == -1 {
		return "", "", errors.New("no valid backtick-enclosed text found")
	}

	// Adjust the start index if it's ```json
	startOffset := 3
	if strings.HasPrefix(response[start:], "```json") {
		startOffset = 7
	}

	// Find the end of the code block
	end := strings.LastIndex(response, "```")
	if end == -1 || start == end {
		return "", "", errors.New("no valid backtick-enclosed text found")
	}

	// Extract text between backticks
	jsonStr := response[start+startOffset : end]

	// Unmarshal the JSON into a map
	var dataMap map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &dataMap)
	if err != nil {
		return "", "", fmt.Errorf("invalid JSON: %v", err)
	}

	// Extract the "name" field
	name, ok := dataMap["name"].(string)
	if !ok {
		return "", "", errors.New("name field not found or not a string")
	}

	return jsonStr, name, nil
}

func templateQuery(data QueryTemplateData) string {
	// Read the embedded template file
	tmpl, err := basequeryBlob.ReadFile("templates/base_query.txt")
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

func templateNetAttachDef(data NetAttachDefTemplateData) string {
	// Read the embedded template file
	tmpl, err := netattachdefBlob.ReadFile("templates/netattachdef_template.txt")
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

// func listNetworkInterfaces() (string, error) {
// 	interfaces, err := net.Interfaces()
// 	if err != nil {
// 		logErr(fmt.Sprintf("%v", err))
// 		return "", fmt.Errorf("error listing interfaces: %v", err)
// 	}

// 	var result string
// 	for _, intf := range interfaces {
// 		result += fmt.Sprintf("%v\n", intf.Name)
// 		// Add more details as needed
// 	}

// 	return result, nil
// }

// func getIPRoute() (string, error) {
// 	cmd := exec.Command("ip", "route")
// 	var out bytes.Buffer
// 	cmd.Stdout = &out
// 	err := cmd.Run()
// 	if err != nil {
// 		return "", err
// 	}
// 	return out.String(), nil
// }

func queryLLM(ollamaHost string, ollamaPort string, ollamaModel string, usedebug bool, query string) (string, error) {
	// Define the URL and payload
	url := "http://" + ollamaHost + ":" + ollamaPort + "/api/generate"
	payload := map[string]string{"model": ollamaModel, "prompt": query}
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

	if usedebug {
		logErr(strings.TrimSpace(finalResponse))
		// logErr("---")
		// logErr(string(responseBody))
	}
	return strings.TrimSpace(finalResponse), nil
}
