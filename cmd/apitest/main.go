// Command apitest checks that the Claude API is reachable and billable before
// any of the agent work depends on it.
//
// It answers three separate questions that are easy to conflate:
//
//	is a key present?        - configuration
//	does the key authorise?  - authentication
//	will calls actually bill? - credit, which is the one that has been in doubt
//
// A 401 means the key is wrong. A 400 about credit means the key is fine but the
// promotional balance does not apply to API usage, which is exactly the question
// left open earlier: subscription credit and API console credit are separate
// systems, and only the latter pays for this.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/vinayaktyagi10/warren/internal/config"
)

func main() {
	provider := flag.String("provider", "gemini", "which backend to check: gemini or claude")
	model := flag.String("model", "", "model to call; empty uses the provider default")
	envFile := flag.String("env", ".env", "file to read the API key from")
	list := flag.Bool("list", false, "list the models the key can reach (gemini only)")
	flag.Parse()

	if err := config.LoadDotEnv(*envFile); err != nil {
		log.Fatalf("read %s: %v", *envFile, err)
	}

	switch *provider {
	case "gemini":
		if *list {
			if err := listGemini(); err != nil {
				log.Fatalf("%v", err)
			}
			return
		}
		m := *model
		if m == "" {
			m = "gemini-flash-latest"
		}
		if err := testGemini(m); err != nil {
			os.Exit(1)
		}
		return
	case "claude":
		m := *model
		if m == "" {
			m = "claude-opus-5"
		}
		testClaude(m)
		return
	default:
		log.Fatalf("unknown provider %q; use gemini or claude", *provider)
	}
}

func testClaude(model string) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		log.Fatalf("ANTHROPIC_API_KEY is not set (looked in the environment and the env file)")
	}
	// Never print the key. The prefix and length are enough to tell a truncated
	// paste from a wrong-account key.
	fmt.Printf("key loaded: %s… (%d chars)\n", safePrefix(key), len(key))

	client := anthropic.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("calling %s…\n\n", model)
	start := time.Now()

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 64,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(
				"Reply with exactly: WARREN API OK")),
		},
	})
	if err != nil {
		diagnose(err)
		os.Exit(1)
	}

	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			fmt.Printf("response: %s\n", strings.TrimSpace(text.Text))
		}
	}

	in := resp.Usage.InputTokens
	out := resp.Usage.OutputTokens
	fmt.Printf("\ntokens:   %d in, %d out\n", in, out)
	fmt.Printf("latency:  %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("cost:     $%.6f at Opus 5 rates ($5/Mtok in, $25/Mtok out)\n",
		float64(in)*5/1e6+float64(out)*25/1e6)
	fmt.Printf("\nthe call billed successfully, so this key can pay for the agent layer\n")
}

// diagnose turns an SDK error into the specific thing that is wrong, since the
// three failure modes need three different fixes.
func diagnose(err error) {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		fmt.Printf("request failed before reaching the API: %v\n", err)
		fmt.Printf("  usually a network or DNS problem rather than anything about the key\n")
		return
	}

	fmt.Printf("API returned %d\n\n", apiErr.StatusCode)
	switch apiErr.StatusCode {
	case 401:
		fmt.Printf("  the key was rejected. It is malformed, revoked, or from another account.\n")
		fmt.Printf("  reissue at platform.claude.com -> API keys and rewrite .env\n")
	case 400:
		fmt.Printf("  %v\n\n", apiErr)
		if strings.Contains(strings.ToLower(apiErr.Error()), "credit") {
			fmt.Printf("  this is the credit question, answered: the key authenticates but the\n")
			fmt.Printf("  account has no API balance. Promotional credit attached to a Claude\n")
			fmt.Printf("  subscription does not fund API usage - they are separate ledgers.\n")
			fmt.Printf("  buy credit under Billing, or confirm with support that the promotion\n")
			fmt.Printf("  was issued as console credit.\n")
		}
	case 403:
		fmt.Printf("  authenticated but not permitted - often the wrong workspace or an\n")
		fmt.Printf("  organisation restriction on the model requested\n")
	case 404:
		fmt.Printf("  model not found. Check the model id; ids carry no date suffix.\n")
	case 429:
		fmt.Printf("  rate limited. The key works; retry shortly.\n")
	default:
		fmt.Printf("  %v\n", apiErr)
	}
}

func safePrefix(key string) string {
	if len(key) < 14 {
		return key[:min(4, len(key))]
	}
	return key[:14]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
