package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/genai"
)

// testGemini checks that the Gemini API is reachable and that the key has quota.
// Gemini is the default backend for the agent layer: its free tier covers this
// project's volume, and unlike the Claude Agent SDK it has an official Go
// client, so the service stays a single Go process.
func testGemini(model string) error {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return fmt.Errorf("GEMINI_API_KEY is not set (looked in the environment and the env file)")
	}
	fmt.Printf("key loaded: %s… (%d chars)\n", safePrefix(key), len(key))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  key,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	fmt.Printf("calling %s…\n\n", model)
	start := time.Now()

	resp, err := client.Models.GenerateContent(ctx, model,
		genai.Text("Reply with exactly: WARREN API OK"), nil)
	if err != nil {
		diagnoseGemini(err)
		return err
	}

	fmt.Printf("response: %s\n", strings.TrimSpace(resp.Text()))
	if u := resp.UsageMetadata; u != nil {
		fmt.Printf("\ntokens:   %d in, %d out, %d total\n",
			u.PromptTokenCount, u.CandidatesTokenCount, u.TotalTokenCount)
	}
	fmt.Printf("latency:  %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("\nthe call succeeded, so the agent layer can run on this key\n")
	return nil
}

// listGemini prints the models this key can actually reach, which is the
// fastest way to resolve a 404 on a model name that has since been renamed.
func listGemini() error {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return fmt.Errorf("GEMINI_API_KEY is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return err
	}
	page, err := client.Models.List(ctx, nil)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	fmt.Printf("models reachable with this key:\n")
	for _, m := range page.Items {
		fmt.Printf("  %s\n", strings.TrimPrefix(m.Name, "models/"))
	}
	return nil
}

func diagnoseGemini(err error) {
	msg := err.Error()
	fmt.Printf("request failed\n\n  %v\n\n", err)
	switch {
	case strings.Contains(msg, "API_KEY_INVALID") || strings.Contains(msg, "401"):
		fmt.Printf("  the key was rejected. Reissue at aistudio.google.com/apikey\n")
	case strings.Contains(msg, "RESOURCE_EXHAUSTED") || strings.Contains(msg, "429"):
		fmt.Printf("  quota exhausted for today. The free tier resets daily; the key is fine.\n")
	case strings.Contains(msg, "404") || strings.Contains(msg, "NOT_FOUND"):
		fmt.Printf("  that model name is not available to this key. Run with -list to see\n")
		fmt.Printf("  which models it can reach.\n")
	case strings.Contains(msg, "PERMISSION_DENIED") || strings.Contains(msg, "403"):
		fmt.Printf("  the key authenticates but is not permitted to use this model.\n")
	}
}
