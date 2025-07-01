package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const (
	homeworkInstructionsFile = "homework0e3.txt"
	compiledExecutableName   = "student_executable"
)

// TestCase and TestCasesResponse structs remain the same
type TestCase struct {
	Description    string `json:"description"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
}

type TestCasesResponse struct {
	TestCases []TestCase `json:"test_cases"`
}

// LanguageConfig is simplified, no more conventional entrypoint needed
type LanguageConfig struct {
	Language    string
	GlobPattern string
	CompileCmd  []string
	ExecuteCmd  []string
}

type Project struct {
	LanguageConfig
	EntryPointFile      string
	EntryPointBaseName  string
	EntryPointClassName string
}

var supportedLanguages = map[string]LanguageConfig{
	"Python": {
		Language:    "Python",
		GlobPattern: "*.py",
		ExecuteCmd:  []string{"python3", "__FILE__"},
	},
	"Java": {
		Language:   "Java",
		GlobPattern: "*.java",
		CompileCmd: []string{"javac", "__FILE__"},
		ExecuteCmd: []string{"java", "-cp", "..", "__CLASSNAME__"},
	},
	"C++": {
		Language:   "C++",
		GlobPattern: "*.cpp",
		CompileCmd: []string{"g++", "__FILE__", "-o", compiledExecutableName, "-std=c++17"},
		ExecuteCmd: []string{"./" + compiledExecutableName},
	},
}

const entryPointPromptTemplate = `You are a code analysis expert. Given the following list of filenames from a student's project, identify the single most likely main entry-point file that should be executed to run the entire program.

Consider common naming conventions (like 'main', 'app', 'run', 'solution') and the presence of a 'main' function if the language requires it.

Respond with ONLY the filename and nothing else.

FILENAMES:
%s`

const testGenPromptTemplate = `You are an expert Test Case Generator AI. Based on the provided homework instructions, create 5 diverse and effective test cases. Your response MUST be a single, valid JSON object.

---
Homework Instructions:
%s
---
`

// askAiForEntryPoint uses Gemini to decide the main file when multiple are present.
func askAiForEntryPoint(ctx context.Context, client *genai.GenerativeModel, files []string) (string, error) {
	fmt.Printf("Multiple potential entry points found: %v. Asking AI for the main file...\n", files)
	
	var fileBasenames []string
	for _, f := range files {
		fileBasenames = append(fileBasenames, filepath.Base(f))
	}

	prompt := fmt.Sprintf(entryPointPromptTemplate, strings.Join(fileBasenames, "\n"))
	resp, err := client.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", fmt.Errorf("gemini failed to determine entry point: %w", err)
	}

	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return "", errors.New("gemini returned an empty response when asked for the entry point")
	}

	aiChoice := string(resp.Candidates[0].Content.Parts[0].(genai.Text))
	aiChoice = strings.TrimSpace(aiChoice)

	// Verify the AI's choice is one of the original files
	for _, basename := range fileBasenames {
		if basename == aiChoice {
			fmt.Printf("AI selected '%s' as the entry point.\n", aiChoice)
			return aiChoice, nil
		}
	}

	return "", fmt.Errorf("AI chose '%s', which is not in the list of found files: %v", aiChoice, fileBasenames)
}

func detectProjectLanguage(ctx context.Context, client *genai.GenerativeModel, repoRoot string) (*Project, error) {
	fmt.Println("Detecting project language and entry point...")
	for _, config := range supportedLanguages {
		globPath := filepath.Join(repoRoot, config.GlobPattern)
		matches, _ := filepath.Glob(globPath)

		if len(matches) == 0 {
			continue
		}

		var selectedBaseName string
		if len(matches) == 1 {
			selectedBaseName = filepath.Base(matches[0])
			fmt.Printf("Detected single %s file: using '%s' as the entry point.\n", config.Language, selectedBaseName)
		} else {
			// More than one file, use AI to decide
			aiChoice, err := askAiForEntryPoint(ctx, client, matches)
			if err != nil {
				return nil, err
			}
			selectedBaseName = aiChoice
		}

		return &Project{
			LanguageConfig:      config,
			EntryPointFile:      filepath.Join(repoRoot, selectedBaseName),
			EntryPointBaseName:  selectedBaseName,
			EntryPointClassName: strings.TrimSuffix(selectedBaseName, filepath.Ext(selectedBaseName)),
		}, nil
	}
	return nil, errors.New("could not detect project language: no files with recognized extensions (*.py, *.java, *.cpp) found")
}

// buildCommand and main function logic follows...
// (The rest of the script is largely the same as the previous version, using the detected 'project' object)

func buildCommand(args []string, project *Project) []string {
	result := make([]string, len(args))
	for i, arg := range args {
		arg = strings.Replace(arg, "__FILE__", project.EntryPointFile, -1)
		arg = strings.Replace(arg, "__CLASSNAME__", project.EntryPointClassName, -1)
		result[i] = arg
	}
	return result
}

func main() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" { log.Fatalf("GEMINI_API_KEY environment variable not set.") }
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey)); 
	if err != nil { log.Fatalf("Error creating Gemini client: %v", err) }
	defer client.Close()
	model := client.GenerativeModel("gemini-1.5-flash")

	repoRoot := ".."
	project, err := detectProjectLanguage(ctx, model, repoRoot)
	if err != nil {
		log.Fatalf("::error::%v", err)
	}

	// 1. Generate Test Cases
	fmt.Println("\nGenerating test cases...")
	// Abbreviated for clarity - this logic is unchanged.
	actualHomeworkInstructionsFile := filepath.Join(repoRoot, homeworkInstructionsFile)
	homeworkInstructions, _ := ioutil.ReadFile(actualHomeworkInstructionsFile)
	prompt := fmt.Sprintf(testGenPromptTemplate, string(homeworkInstructions))
	resp, _ := model.GenerateContent(ctx, genai.Text(prompt))
	jsonPart := resp.Candidates[0].Content.Parts[0].(genai.Text); jsonStr := strings.Trim(string(jsonPart), " \n\t`json")
	var testCasesResponse TestCasesResponse
	_ = json.Unmarshal([]byte(jsonStr), &testCasesResponse)
	fmt.Printf("Successfully generated %d test cases.\n", len(testCasesResponse.TestCases))

	// 2. Compile code if necessary
	if project.CompileCmd != nil {
		cmdArgs := buildCommand(project.CompileCmd, project)
		fmt.Printf("\nCompiling student code: %s\n", strings.Join(cmdArgs, " "))
		cmdBuild := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		buildOutput, err := cmdBuild.CombinedOutput()
		if err != nil {
			log.Fatalf("::error::Failed to compile student code. Error: %v\nCompiler Output:\n%s", err, string(buildOutput))
		}
		fmt.Println("Compilation successful.")
	}

	// 3. Run against each test case
	// This logic is unchanged.
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

	// 4. Final Report
	fmt.Println("\n--- Functional Test Summary ---")
	summary := fmt.Sprintf("Passed %d out of %d test cases for the %s project.", len(testCasesResponse.TestCases)-failedTests, len(testCasesResponse.TestCases), project.Language)
	if failedTests > 0 {
		fmt.Println("::error::" + summary)
		os.Exit(1)
	}
	fmt.Println(summary)
}