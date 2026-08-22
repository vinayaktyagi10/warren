package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// GeminiAssessor asks a model to read the ring's evidence and say what it makes
// of it.
//
// The response is constrained by a schema with the action as an enum, so a
// malformed or invented action is rejected by the API rather than parsed out of
// prose here. That is a guard, not the guarantee: Policy.Apply still clamps
// whatever arrives, because schema conformance says a value is well-formed, not
// that it is warranted.
type GeminiAssessor struct {
	client *genai.Client
	model  string
}

// DefaultGeminiModel reasons before answering, which is the point here — the
// task is a judgement about a pattern, not a lookup. Measured at ~2.6s and ~97
// tokens per call against ~0.8s for the non-reasoning lite model.
const DefaultGeminiModel = "gemini-3.7-flash"

func NewGeminiAssessor(ctx context.Context, apiKey, model string) (*GeminiAssessor, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no API key")
	}
	if model == "" {
		model = DefaultGeminiModel
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &GeminiAssessor{client: client, model: model}, nil
}

func (g *GeminiAssessor) Name() string { return "gemini:" + g.model }

// systemInstruction fixes the assessor's job and its limits.
//
// It states the bounds even though the bounds are enforced in code regardless.
// Telling the model the rules produces better-calibrated proposals and fewer
// pointless overrides; not relying on it having followed them is what keeps the
// system safe when it does not.
const systemInstruction = `You assess suspected money-laundering rings for a payments risk team.

You are given measured facts about a group of accounts that transacted together.
Judge whether the pattern is consistent with coordinated laundering or with
ordinary business activity, and recommend one action.

The three actions:
- allow: the pattern is unremarkable and the transfers should stand.
- hold_for_review: the pattern warrants a human investigator reading it.
- block: the pattern is clear enough to stop the transfers outright.

What the numbers mean:
- conservation: how closely value entering an intermediary account leaves it
  again. Near 1 means accounts forward almost exactly what they receive, which
  is how mules behave. Ordinary accounts keep or top up a balance.
- pass_through: the share of accounts that both receive and send.
- score: a ranker's calibrated probability that this group is a ring, learned
  from labelled cases.
- typology: the shape the group forms. FAN-OUT is one account paying many;
  FAN-IN is many paying one; CYCLE is value returning to its origin; MIXED means
  the shape was not resolved.

Be concrete and cite the specific figures that drove your view. State what would
change your mind. If the evidence is ordinary, say so plainly — a high alert rate
costs real people access to their money, and recommending review for everything
is not caution, it is abdication.

Your recommendation is advisory. A policy layer independently gates blocking on
detector score, on your stated confidence, and on the sum involved, and will
downgrade a recommendation that does not clear those bars. Recommend what the
evidence supports and let the policy do its job.

Confidence is your own certainty in the recommendation, from 0 to 1.`

func (g *GeminiAssessor) Assess(ctx context.Context, ev Evidence) (Proposal, error) {
	schema := &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"action": {
				Type:        genai.TypeString,
				Enum:        []string{string(ActionAllow), string(ActionHold), string(ActionBlock)},
				Description: "The recommended action.",
			},
			"confidence": {
				Type:        genai.TypeNumber,
				Minimum:     genai.Ptr(0.0),
				Maximum:     genai.Ptr(1.0),
				Description: "Certainty in the recommendation, 0 to 1.",
			},
			"rationale": {
				Type: genai.TypeString,
				Description: "Two to four sentences an investigator can act on, citing the " +
					"specific figures that drove the view.",
			},
		},
		Required: []string{"action", "confidence", "rationale"},
	}

	resp, err := g.client.Models.GenerateContent(ctx, g.model, genai.Text(renderEvidence(ev)),
		&genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: systemInstruction}}},
			ResponseMIMEType:  "application/json",
			ResponseSchema:    schema,
			Temperature:       genai.Ptr(float32(0.0)),
			MaxOutputTokens:   2048,
		})
	if err != nil {
		return Proposal{}, fmt.Errorf("generate: %w", err)
	}

	text := strings.TrimSpace(resp.Text())
	if text == "" {
		return Proposal{}, fmt.Errorf("empty response")
	}

	var out struct {
		Action     string  `json:"action"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return Proposal{}, fmt.Errorf("parse response: %w", err)
	}

	return Proposal{
		Action:     Action(out.Action),
		Confidence: out.Confidence,
		Rationale:  strings.TrimSpace(out.Rationale),
	}, nil
}

// renderEvidence writes the facts as plain labelled figures.
//
// Every value is a number the detector computed. Nothing an account holder
// controls — a name, a reference, a memo field — reaches the prompt, so there is
// no channel here through which a party under investigation could address the
// model that is judging them.
func renderEvidence(ev Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Suspected ring %d\n\n", ev.RingID)
	fmt.Fprintf(&b, "shape:            %s\n", ev.Typology)
	fmt.Fprintf(&b, "accounts:         %d\n", ev.Accounts)
	fmt.Fprintf(&b, "transfers:        %d\n", ev.Txns)
	fmt.Fprintf(&b, "total moved:      %.2f\n", ev.TotalAmount)
	fmt.Fprintf(&b, "largest transfer: %.2f\n", ev.MaxAmount)
	fmt.Fprintf(&b, "window:           %.1f hours\n", ev.SpanHours)
	fmt.Fprintf(&b, "conservation:     %.3f\n", ev.Conservation)
	fmt.Fprintf(&b, "pass_through:     %.3f\n", ev.PassThrough)
	fmt.Fprintf(&b, "score:            %.3f\n", ev.Score)
	return b.String()
}
