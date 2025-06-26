package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const (
	homeworkInstructionsFile = "homework0e3.txt" 
	geminiPromptTemplate     = `You are an AI assistant specialized in evaluating code against homework instructions.
Your task is to analyze the provided code snippets (which may be in various programming languages) and determine if they correctly implement the requirements described in the homework instructions.
Focus on correctness, completeness, and adherence to the problem statement. Do not focus on style unless explicitly mentioned in the instructions.

---
Homework Instructions:
%s
---

---
Provided Code Files:
%s
---

Based on the above, please provide a concise evaluation.
If the code is correct and complete according to the instructions, state "APPROVED" and provide a brief justification.
If there are issues, state "REJECTED" and explain clearly what needs to be fixed or improved.
Be specific and actionable in your feedback, referencing specific parts of the code or instructions if necessary.
`
)

type GitHubPushEvent struct {
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Commits []struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

type GitHubCommitDetails struct {
	Files []struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	} `json:"files"`
}

func main() {
	// 1. Read the GITHUB_EVENT_PATH payload
	githubEventPath := os.Getenv("GITHUB_EVENT_PATH")
	if githubEventPath == "" {
		log.Fatalf("GITHUB_EVENT_PATH environment variable not set. This script should run in a GitHub Actions workflow.")
	}

	eventPayloadBytes, err := ioutil.ReadFile(githubEventPath)
	if err != nil {
		log.Fatalf("Failed to read GITHUB_EVENT_PATH: %v", err)
	}

	var pushEvent GitHubPushEvent
	if err := json.Unmarshal(eventPayloadBytes, &pushEvent); err != nil {
		log.Fatalf("Failed to unmarshal GitHub push event payload: %v", err)
	}

	// --- START: Fetch file changes via GitHub API ---
	headCommitSHA := pushEvent.HeadCommit.ID
	repoOwner := pushEvent.Repository.Owner.Login
	repoName := pushEvent.Repository.Name

	if headCommitSHA == "" || repoOwner == "" || repoName == "" {
		log.Fatalf("Could not extract necessary information (commit SHA, repo owner, or repo name) from GITHUB_EVENT_PATH for GitHub API call.")
	}

	fmt.Printf("Fetching commit details for %s/%s@%s via GitHub API...\n", repoOwner, repoName, headCommitSHA)

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		log.Fatalf("GITHUB_TOKEN environment variable not set. It is required for GitHub API calls. Ensure your workflow has 'permissions: contents: read'.")
	}

	httpClient := &http.Client{}
	req, err := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", repoOwner, repoName, headCommitSHA), nil)
	if err != nil {
		log.Fatalf("Error creating GitHub API request: %v", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28") 

	respAPI, err := httpClient.Do(req)
	if err != nil {
		log.Fatalf("Error making GitHub API request: %v", err)
	}
	defer respAPI.Body.Close()

	if respAPI.StatusCode != http.StatusOK {
		bodyBytes, _ := ioutil.ReadAll(respAPI.Body)
		log.Fatalf("GitHub API request failed with status %d for commit %s: %s", respAPI.StatusCode, headCommitSHA, string(bodyBytes))
	}

	var commitDetails GitHubCommitDetails
	apiResponseBody, err := ioutil.ReadAll(respAPI.Body)
	if err != nil {
		log.Fatalf("Error reading GitHub API response body: %v", err)
	}
	if err := json.Unmarshal(apiResponseBody, &commitDetails); err != nil {
		log.Fatalf("Error unmarshaling GitHub API response: %v", err)
	}

	var changedFilesRepoRelative []string 
	for _, file := range commitDetails.Files {
		// Only consider "added" or "modified" files for grading
		if file.Status == "added" || file.Status == "modified" {
			changedFilesRepoRelative = append(changedFilesRepoRelative, file.Filename)
		}
	}
	// --- END: Fetch file changes via GitHub API ---

	repoRootPrefix := ".." + string(filepath.Separator)

	filteredFiles := []string{}
	for _, fileRepoRelative := range changedFilesRepoRelative {
		// Skip internal grader files (paths will be like 'correctness-tester/main.go' from API)
		// We use strings.HasPrefix for folder names, and exact match for files like homework.txt
		if strings.HasPrefix(fileRepoRelative, "correctness-tester/") ||
			strings.HasPrefix(fileRepoRelative, ".github/workflows/") ||
			fileRepoRelative == homeworkInstructionsFile {
			log.Printf("Skipping internal/config file: %s", fileRepoRelative)
			continue
		}

		pathForReading := fileRepoRelative
		if !strings.HasPrefix(fileRepoRelative, "correctness-tester/") {
			pathForReading = filepath.Join(repoRootPrefix, fileRepoRelative)
		}

		filteredFiles = append(filteredFiles, pathForReading)
	}

	if len(filteredFiles) == 0 {
		fmt.Println("No relevant code files found for evaluation after filtering. Skipping evaluation.")
		os.Exit(0)
	}

	// 2. Read Homework Instructions
	// Homework instructions file is at the repo root, so its path needs to be adjusted relative to the script's CWD
	actualHomeworkInstructionsFile := filepath.Join(repoRootPrefix, homeworkInstructionsFile)
	homeworkInstructions, err := ioutil.ReadFile(actualHomeworkInstructionsFile)
	if err != nil {
		log.Fatalf("Error reading homework instructions file '%s': %v", actualHomeworkInstructionsFile, err)
	}

	// 3. Read Changed Code Files
	var allCodeContent strings.Builder
	for _, fileAdjustedPath := range filteredFiles {
		code, err := ioutil.ReadFile(fileAdjustedPath)
		if err != nil {
			log.Printf("Warning: Could not read file '%s': %v", fileAdjustedPath, err)
			continue
		}
		// The header should use the original repo-relative path for Gemini's context
		// We need to strip the '..' prefix if it was added for display.
		displayFileName := strings.TrimPrefix(fileAdjustedPath, repoRootPrefix)
		allCodeContent.WriteString(fmt.Sprintf("--- File: %s ---\n", displayFileName))
		allCodeContent.Write(code)
		allCodeContent.WriteString("\n\n")
	}

	if allCodeContent.Len() == 0 {
		fmt.Println("No code could be read from the changed files. Skipping evaluation.")
		os.Exit(0)
	}

	// 4. Construct Prompt
	prompt := fmt.Sprintf(geminiPromptTemplate, string(homeworkInstructions), allCodeContent.String())

	// 5. Call Gemini API
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatalf("GEMINI_API_KEY environment variable not set. Please add it as a GitHub Secret.")
	}

	ctx := context.Background()
	clientGenAI, err := genai.NewClient(ctx, option.WithAPIKey(apiKey)) // Renamed client to avoid http.Client name conflict
	if err != nil {
		log.Fatalf("Error creating Gemini client: %v", err)
	}
	defer clientGenAI.Close()

	model := clientGenAI.GenerativeModel("gemini-2.0-flash") 
	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		log.Fatalf("Error generating content from Gemini: %v", err)
	}

	// 6. Process and Output Gemini Feedback
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		var geminiOutput strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				geminiOutput.WriteString(string(txt))
			}
		}
		feedback := geminiOutput.String()

		fmt.Println("--- Gemini Feedback ---")
		fmt.Println(feedback)
		fmt.Println("-----------------------")

		// Output for GitHub Actions: Set as a step summary
		summaryFilePath := os.Getenv("GITHUB_STEP_SUMMARY")
		if summaryFilePath != "" {
			err := ioutil.WriteFile(summaryFilePath, []byte(feedback), 0644)
			if err != nil {
				log.Printf("Warning: Failed to write to GITHUB_STEP_SUMMARY: %v", err)
			}
		} else {
			log.Println("GITHUB_STEP_SUMMARY not found. Outputting feedback to stdout.")
		}

		// Optionally, if the feedback is "REJECTED", make the GitHub Action fail.
		if strings.Contains(strings.ToUpper(feedback), "REJECTED") {
			fmt.Println("::error::Code was rejected by Gemini. Check the step summary for details.")
			os.Exit(1) // Fail the GitHub Action
		}

	} else {
		fmt.Println("Gemini did not return any content.")
		fmt.Println("::error::Gemini did not return any content for the evaluation.")
		os.Exit(1)
	}
}