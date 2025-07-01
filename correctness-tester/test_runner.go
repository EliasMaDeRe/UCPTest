package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

// --- Structs and Constants ---
const (
	homeworkInstructionsFile = "homework0e3.txt"
	compiledExecutableName   = "student_executable"
)
type TestCase struct {
	Description    string `json:"description"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
}
type TestCasesResponse struct{ TestCases []TestCase `json:"test_cases"` }
type LanguageConfig struct {
	Language string; GlobPattern string; CompileCmd []string; ExecuteCmd []string
}
type Project struct {
	LanguageConfig; EntryPointFile string; EntryPointBaseName string; EntryPointClassName string
}
type GitHubPushEvent struct {
	HeadCommit struct { ID string `json:"id"` } `json:"head_commit"`
	Repository struct { Name  string `json:"name"`; Owner struct { Login string `json:"login"` } `json:"owner"` } `json:"repository"`
}
type GitHubCommitDetails struct {
	Files []struct { Filename string `json:"filename"`; Status string `json:"status"` } `json:"files"`
}

// --- Language and Prompt Configurations ---
var supportedLanguages = map[string]LanguageConfig{
	"Python": { Language: "Python", GlobPattern: "*.py", ExecuteCmd: []string{"python3", "__FILE__"} },
	"Java":   { Language: "Java", GlobPattern: "*.java", CompileCmd: []string{"javac", "__FILE__"}, ExecuteCmd: []string{"java", "-cp", "..", "__CLASSNAME__"} },
	"C++":    { Language: "C++", GlobPattern: "*.cpp", CompileCmd: []string{"g++", "__FILE__", "-o", compiledExecutableName, "-std=c++17"}, ExecuteCmd: []string{"./" + compiledExecutableName} },
}
const entryPointPromptTemplate = `You are a code analysis expert. Given the following list of filenames from a student's project, identify the single most likely main entry-point file. Respond with ONLY the filename and nothing else. FILENAMES: %s`
const testGenPromptTemplate = `You are an expert Test Case Generator AI. Based on the provided homework instructions, create 5 diverse and effective test cases. Your response MUST be a single, valid JSON object.
---
Homework Instructions:
%s
---
`
// --- Helper functions ---
func askAiForEntryPoint(ctx context.Context, client *genai.GenerativeModel, files []string) (string, error) {
	fmt.Printf("Multiple potential entry points found: %v. Asking AI for the main file...\n", files)
	var fileBasenames []string
	for _, f := range files { fileBasenames = append(fileBasenames, filepath.Base(f)) }
	prompt := fmt.Sprintf(entryPointPromptTemplate, strings.Join(fileBasenames, "\n"))
	resp, err := client.GenerateContent(ctx, genai.Text(prompt))
	if err != nil { return "", fmt.Errorf("gemini failed to determine entry point: %w", err) }
	if resp == nil || len(resp.Candidates) == 0 { return "", errors.New("gemini returned an empty response for entry point") }
	aiChoice := strings.TrimSpace(string(resp.Candidates[0].Content.Parts[0].(genai.Text)))
	for _, basename := range fileBasenames { if basename == aiChoice { fmt.Printf("AI selected '%s' as the entry point.\n", aiChoice); return aiChoice, nil } }
	return "", fmt.Errorf("AI chose '%s', which is not in the list of found files: %v", aiChoice, fileBasenames)
}

func buildCommand(args []string, project *Project) []string {
	result := make([]string, len(args))
	for i, arg := range args {
		arg = strings.Replace(arg, "__FILE__", project.EntryPointFile, -1)
		arg = strings.Replace(arg, "__CLASSNAME__", project.EntryPointClassName, -1)
		result[i] = arg
	}
	return result
}

// --- Main application logic ---
func main() {
	// 1. READ PUSH EVENT TO GET CHANGED FILES
	fmt.Println("Reading GitHub push event...")
	githubEventPath := os.Getenv("GITHUB_EVENT_PATH")
	if githubEventPath == "" { log.Fatalf("GITHUB_EVENT_PATH environment variable not set.") }
	eventPayloadBytes, err := ioutil.ReadFile(githubEventPath)
	if err != nil { log.Fatalf("Failed to read GITHUB_EVENT_PATH: %v", err) }
	var pushEvent GitHubPushEvent
	if err := json.Unmarshal(eventPayloadBytes, &pushEvent); err != nil { log.Fatalf("Failed to unmarshal GitHub push event payload: %v", err) }
	
	headCommitSHA := pushEvent.HeadCommit.ID
	repoOwner := pushEvent.Repository.Owner.Login
	repoName := pushEvent.Repository.Name
	if headCommitSHA == "" || repoOwner == "" || repoName == "" { log.Fatalf("Could not extract commit SHA, repo owner, or repo name from event payload.") }

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" { log.Fatalf("GITHUB_TOKEN environment variable not set.") }

	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", repoOwner, repoName, headCommitSHA), nil)
	req.Header.Set("Authorization", "token "+githubToken)
	respAPI, err := (&http.Client{}).Do(req)
	if err != nil { log.Fatalf("Error making GitHub API request: %v", err) }
	defer respAPI.Body.Close()
	if respAPI.StatusCode != http.StatusOK { bodyBytes, _ := ioutil.ReadAll(respAPI.Body); log.Fatalf("GitHub API request failed with status %d: %s", respAPI.StatusCode, string(bodyBytes)) }
	
	var commitDetails GitHubCommitDetails
	if err := json.NewDecoder(respAPI.Body).Decode(&commitDetails); err != nil { log.Fatalf("Error unmarshaling GitHub API response: %v", err) }

	// 2. DETERMINE LANGUAGE FROM CHANGED FILES
	var detectedLangConfig LanguageConfig
	var relevantFilesChanged []string
	for _, changedFile := range commitDetails.Files {
		ext := filepath.Ext(changedFile.Filename)
		// *** THIS IS THE CORRECTED LINE ***
		for _, config := range supportedLanguages {
			if strings.TrimPrefix(ext, ".") == strings.TrimPrefix(config.GlobPattern, "*.") {
				detectedLangConfig = config
				relevantFilesChanged = append(relevantFilesChanged, changedFile.Filename)
			}
		}
	}

	if detectedLangConfig.Language == "" {
		fmt.Println("No relevant code files (.py, .java, .cpp) changed in this push. Skipping functional tests.")
		os.Exit(0)
	}
	fmt.Printf("Detected changes to %s files: %v\n", detectedLangConfig.Language, relevantFilesChanged)


	// 3. FIND THE MAIN ENTRY POINT FOR THE DETECTED LANGUAGE
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" { log.Fatalf("GEMINI_API_KEY environment variable not set.") }
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey));
	if err != nil { log.Fatalf("Error creating Gemini client: %v", err) }
	defer client.Close()
	model := client.GenerativeModel("gemini-1.5-flash")

	repoRoot := ".."
	globPath := filepath.Join(repoRoot, detectedLangConfig.GlobPattern)
	allProjectFiles, _ := filepath.Glob(globPath)
	if len(allProjectFiles) == 0 { log.Fatalf("Detected %s change, but could not find any %s files in the repo.", detectedLangConfig.Language, detectedLangConfig.GlobPattern) }
	
	var selectedBaseName string
	if len(allProjectFiles) == 1 {
		selectedBaseName = filepath.Base(allProjectFiles[0])
		fmt.Printf("Found single %s file: using '%s' as the entry point.\n", detectedLangConfig.Language, selectedBaseName)
	} else {
		aiChoice, err := askAiForEntryPoint(ctx, model, allProjectFiles)
		if err != nil { log.Fatalf("::error::%v", err) }
		selectedBaseName = aiChoice
	}
	
	project := &Project{
		LanguageConfig:      detectedLangConfig,
		EntryPointFile:      filepath.Join(repoRoot, selectedBaseName),
		EntryPointBaseName:  selectedBaseName,
		EntryPointClassName: strings.TrimSuffix(selectedBaseName, filepath.Ext(selectedBaseName)),
	}

	// 4. GENERATE TEST CASES & RUN THEM
	fmt.Println("\nGenerating test cases...")
	actualHomeworkInstructionsFile := filepath.Join(repoRoot, homeworkInstructionsFile)
	homeworkInstructions, _ := ioutil.ReadFile(actualHomeworkInstructionsFile)
	prompt := fmt.Sprintf(testGenPromptTemplate, string(homeworkInstructions))
	resp, _ := model.GenerateContent(ctx, genai.Text(prompt))
	jsonPart := resp.Candidates[0].Content.Parts[0].(genai.Text); jsonStr := strings.Trim(string(jsonPart), " \n\t`json")
	var testCasesResponse TestCasesResponse
	_ = json.Unmarshal([]byte(jsonStr), &testCasesResponse)
	fmt.Printf("Successfully generated %d test cases.\n", len(testCasesResponse.TestCases))

	if project.CompileCmd != nil {
		cmdArgs := buildCommand(project.CompileCmd, project)
		fmt.Printf("\nCompiling student code: %s\n", strings.Join(cmdArgs, " "))
		cmdBuild := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		buildOutput, err := cmdBuild.CombinedOutput()
		if err != nil { log.Fatalf("::error::Failed to compile student code. Error: %v\nCompiler Output:\n%s", err, string(buildOutput)) }
		fmt.Println("Compilation successful.")
	}

	var failedTests int
	execArgs := buildCommand(project.ExecuteCmd, project)
	for i, tc := range testCasesResponse.TestCases {
		fmt.Printf("\n--- Running Test Case %d: %s ---\n", i+1, tc.Description)
		cmdRun := exec.Command(execArgs[0], execArgs[1:]...)
		cmdRun.Stdin = strings.NewReader(tc.Input)
		var stdout, stderr bytes.Buffer
		cmdRun.Stdout = &stdout
		cmdRun.Stderr = &stderr
		err := cmdRun.Run()
		actualOutput := strings.TrimSpace(stdout.String())
		expectedOutput := strings.TrimSpace(tc.ExpectedOutput)
		fmt.Printf("Input: '%s'\nExpected Output: '%s'\nActual Output:   '%s'\n", tc.Input, expectedOutput, actualOutput)
		if err == nil && actualOutput == expectedOutput {
			fmt.Println("Result: PASSED")
		} else {
			fmt.Println("Result: FAILED")
			failedTests++
		}
	}

	fmt.Println("\n--- Functional Test Summary ---")
	summary := fmt.Sprintf("Passed %d out of %d test cases for the %s project.", len(testCasesResponse.TestCases)-failedTests, len(testCasesResponse.TestCases), project.Language)
	if failedTests > 0 {
		fmt.Println("::error::" + summary)
		os.Exit(1)
	}
	fmt.Println(summary)
}