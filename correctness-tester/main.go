package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const (
	homeworkInstructionsFile = "homework0e3.txt"
	geminiPromptTemplate = `You are an AI assistant specialized in evaluating code against homework instructions.
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
	Commits []struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func main() {

	// 1. Get changed files from the GITHUB_EVENT_PATH payload
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

	var changedFiles []string
	for _, commit := range pushEvent.Commits {
		changedFiles = append(changedFiles, commit.Added...)
		changedFiles = append(changedFiles, commit.Modified...)
	}

	filteredFiles := []string{}
	for _, file := range changedFiles {
		if strings.HasPrefix(file, "correctness-tester/") || strings.HasPrefix(file, ".github/workflows/") {
			log.Printf("Skipping internal file: %s", file)
			continue
		}
		filteredFiles = append(filteredFiles, file)
	}

	if len(filteredFiles) == 0 {
		fmt.Println("No relevant code files changed in this push. Skipping evaluation.")
		os.Exit(0)
	}

	// 2. Read Homework Instructions
	homeworkInstructions, err := ioutil.ReadFile(homeworkInstructionsFile)
	if err != nil {
		log.Fatalf("Error reading homework instructions file '%s': %v", homeworkInstructionsFile, err)
	}

	// 3. Read Changed Code Files
	var allCodeContent strings.Builder
	for _, file := range filteredFiles {
		code, err := ioutil.ReadFile(file)
		if err != nil {
			log.Printf("Warning: Could not read file '%s': %v", file, err)
			continue
		}
		// Add a header to clearly separate files for Gemini
		allCodeContent.WriteString(fmt.Sprintf("--- File: %s ---\n", file))
		allCodeContent.Write(code)
		allCodeContent.WriteString("\n\n") // Add extra newlines for separation
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
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatalf("Error creating Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-pro") // Consider gemini-1.5-flash for potentially faster/cheaper responses
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