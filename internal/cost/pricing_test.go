package cost

import (
	"errors"
	"math"
	"testing"
)

func TestCostOpus(t *testing.T) {
	u := Usage{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 2000,
		CacheReadTokens:     3000,
	}
	cost, err := Cost("claude-opus-4-6", u)
	if err != nil {
		t.Fatal(err)
	}
	// 1000/1M * 15 + 500/1M * 75 + 2000/1M * 18.75 + 3000/1M * 1.50
	// = 0.015 + 0.0375 + 0.0375 + 0.0045
	// = 0.0945
	expected := 0.0945
	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("expected cost %.4f, got %.4f", expected, cost)
	}
}

func TestCostSonnet(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, OutputTokens: 100_000}
	cost, err := Cost("claude-sonnet-4-6", u)
	if err != nil {
		t.Fatal(err)
	}
	// 1M/1M * 3 + 100K/1M * 15 = 3 + 1.5 = 4.5
	expected := 4.5
	if math.Abs(cost-expected) > 0.01 {
		t.Errorf("expected cost %.2f, got %.2f", expected, cost)
	}
}

func TestCostUnknownModel(t *testing.T) {
	u := Usage{InputTokens: 100}
	_, err := Cost("gpt-4", u)
	if !errors.Is(err, ErrUnknownModel) {
		t.Errorf("expected ErrUnknownModel, got %v", err)
	}
}

func TestCostPrefixMatch(t *testing.T) {
	// Model with suffix like "[1m]"
	u := Usage{InputTokens: 1000, OutputTokens: 500}
	_, err := Cost("claude-opus-4-6[1m]", u)
	if err != nil {
		t.Errorf("prefix match should work: %v", err)
	}
}

func TestAllModels(t *testing.T) {
	models := AllModels()
	if len(models) < 3 {
		t.Errorf("expected at least 3 models, got %d", len(models))
	}
}

func TestCostZeroUsage(t *testing.T) {
	cost, err := Cost("claude-opus-4-6", Usage{})
	if err != nil {
		t.Fatal(err)
	}
	if cost != 0 {
		t.Errorf("expected 0 cost for zero usage, got %f", cost)
	}
}
