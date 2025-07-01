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
type TestCase struct { Description string `json:"description"`; Input string `json:"input"`; ExpectedOutput string `json:"expected_output"` }
type TestCasesResponse struct{ TestCases []TestCase `json:"test_cases"` }
type LanguageConfig struct { Language string; FileExtension string; Compiler string; ExecuteCmd []string }
type Project struct { LanguageConfig; EntryPointFile string; EntryPointClassName string }
type GitHubPushEvent struct { HeadCommit struct { ID string `json:"id"` } `json:"head_commit"`; Repository struct { Name  string `json:"name"`; Owner struct { Login string `json:"login"` } `json:"owner"` } `json:"repository"` }
type GitHubCommitDetails struct { Files []struct { Filename string `json:"filename"` } `json:"files"` }

// --- Language and Prompt Configurations ---
var supportedLanguages = map[string]LanguageConfig{
	"Python": { Language: "Python", FileExtension: "py", ExecuteCmd: []string{"python3", "__FILE__"} },
	"Java":   { Language: "Java", FileExtension: "java", Compiler: "javac", ExecuteCmd: []string{"java", "-cp", "..", "__CLASSNAME__"} },
	"C++":    { Language: "C++", FileExtension: "cpp", Compiler: "g++", ExecuteCmd: []string{"./" + compiledExecutableName} },
}
const entryPointPromptTemplate = `You are a code analysis expert. From the following list of filenames that were just pushed in a commit, identify the single most likely main entry-point file. Respond with ONLY the filename and nothing else. FILENAMES: %s`
const testGenPromptTemplate = `You are an expert Test Case Generator AI. Based on the provided homework instructions, create 5 diverse and effective test cases. Your response MUST be a single, valid JSON object.
---
Homework Instructions:
%s
---
`

// --- Helper Functions ---
func askAiForEntryPoint(ctx context.Context, model *genai.GenerativeModel, files []string) (string, error) {
	fmt.Printf("Multiple files pushed: %v. Asking AI for the main file...\n", files)
	prompt := fmt.Sprintf(entryPointPromptTemplate, strings.Join(files, "\n"))
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil { return "", fmt.Errorf("gemini failed to determine entry point: %w", err) }
	if resp == nil || len(resp.Candidates) == 0 { return "", errors.New("gemini returned an empty response for entry point") }
	aiChoice := strings.TrimSpace(string(resp.Candidates[0].Content.Parts[0].(genai.Text)))
	for _, f := range files { if f == aiChoice { fmt.Printf("AI selected '%s' as the entry point.\n", aiChoice); return aiChoice, nil } }
	return "", fmt.Errorf("AI chose '%s', which is not in the list of pushed files: %v", aiChoice, files)
}

func main() {
	// 1. Get Changed Files from GitHub Push Event
	fmt.Println("Reading GitHub push event...")
	// ... (GitHub API logic is unchanged, resulting in `commitDetails.Files`)
	githubEventPath := os.Getenv("GITHUB_EVENT_PATH")
	if githubEventPath == "" { log.Fatalf("GITHUB_EVENT_PATH not set.") }
	eventPayloadBytes, err := ioutil.ReadFile(githubEventPath)
	if err != nil { log.Fatalf("Failed to read GITHUB_EVENT_PATH: %v", err) }
	var pushEvent GitHubPushEvent
	if err := json.Unmarshal(eventPayloadBytes, &pushEvent); err != nil { log.Fatalf("Failed to unmarshal GitHub push event: %v", err) }
	headCommitSHA := pushEvent.HeadCommit.ID; repoOwner := pushEvent.Repository.Owner.Login; repoName := pushEvent.Repository.Name
	githubToken := os.Getenv("GITHUB_TOKEN"); if githubToken == "" { log.Fatalf("GITHUB_TOKEN not set.") }
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", repoOwner, repoName, headCommitSHA), nil)
	req.Header.Set("Authorization", "token "+githubToken)
	respAPI, err := (&http.Client{}).Do(req); if err != nil { log.Fatalf("GitHub API request error: %v", err) }; defer respAPI.Body.Close()
	var commitDetails GitHubCommitDetails
	if err := json.NewDecoder(respAPI.Body).Decode(&commitDetails); err != nil { log.Fatalf("GitHub API unmarshal error: %v", err) }

	// 2. Filter for Relevant Code Files from the Push
	var relevantCodeFiles []string
	var detectedLangConfig LanguageConfig
	for _, changedFile := range commitDetails.Files {
		ext := strings.TrimPrefix(filepath.Ext(changedFile.Filename), ".")
		for _, config := range supportedLanguages {
			if ext == config.FileExtension {
				if detectedLangConfig.Language == "" {
					detectedLangConfig = config
				}
				// Ensure all pushed files are of the same language
				if detectedLangConfig.Language != config.Language {
					log.Fatalf("::error::Mixed language push detected. Please push files of only one language at a time.")
				}
				relevantCodeFiles = append(relevantCodeFiles, changedFile.Filename)
			}
		}
	}

	if len(relevantCodeFiles) == 0 {
		fmt.Println("No relevant source code files changed in this push. Skipping tests.")
		os.Exit(0)
	}
	fmt.Printf("Detected %s files in push: %v\n", detectedLangConfig.Language, relevantCodeFiles)

	// 3. Determine Entry Point ONLY from Pushed Files
	apiKey := os.Getenv("GEMINI_API_KEY"); if apiKey == "" { log.Fatalf("GEMINI_API_KEY not set.") }
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey)); if err != nil { log.Fatalf("Gemini client error: %v", err) }; defer client.Close()
	model := client.GenerativeModel("gemini-1.5-flash")

	var entryPointBaseName string
	if len(relevantCodeFiles) == 1 {
		entryPointBaseName = relevantCodeFiles[0]
		fmt.Printf("Single file pushed. Using '%s' as the entry point.\n", entryPointBaseName)
	} else {
		choice, err := askAiForEntryPoint(ctx, model, relevantCodeFiles)
		if err != nil { log.Fatalf("::error::%v", err) }
		entryPointBaseName = choice
	}

	project := &Project{
		LanguageConfig:      detectedLangConfig,
		EntryPointFile:      filepath.Join("..", entryPointBaseName),
		EntryPointClassName: strings.TrimSuffix(entryPointBaseName, "."+detectedLangConfig.FileExtension),
	}

	// 4. Generate Test Cases (Unchanged)
	fmt.Println("\nGenerating test cases...")
	// ... (abbreviated for clarity) ...
	
	// 5. Compile ALL Pushed Source Files Together
	if project.Compiler != "" {
		// Build the compile command: compiler + all relevant files + output flags
		repoRoot := ".."
		cmdArgs := []string{project.Compiler}
		for _, file := range relevantCodeFiles {
			cmdArgs = append(cmdArgs, filepath.Join(repoRoot, file))
		}
		if project.Language == "C++" {
			cmdArgs = append(cmdArgs, "-o", compiledExecutableName, "-std=c++17")
		}
		
		fmt.Printf("\nCompiling pushed files: %s\n", strings.Join(cmdArgs, " "))
		cmdBuild := exec.Command(cmdArgs[0], cmdArgs[1:]...)
		buildOutput, err := cmdBuild.CombinedOutput()
		if err != nil { log.Fatalf("::error::Failed to compile. Output:\n%s", string(buildOutput)) }
		fmt.Println("Compilation successful.")
	}
	
	// 6. Execute and Test (Unchanged)
	// ... (The test loop remains the same) ...
	var execCmd []string
	for _, arg := range project.ExecuteCmd {
		arg = strings.Replace(arg, "__FILE__", project.EntryPointFile, -1)
		arg = strings.Replace(arg, "__CLASSNAME__", project.EntryPointClassName, -1)
		execCmd = append(execCmd, arg)
	}
	// ... loop through test cases using 'execCmd' ...
}