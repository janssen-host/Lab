package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/charmbracelet/lipgloss"
)

//go:embed log-colours.json
var defaultLogColours []byte

type colourRule struct {
	pattern *regexp.Regexp
	colour  lipgloss.Color
}

type colourConfig struct {
	Colours  map[string]string `json:"colours"`
	Patterns []struct {
		Colour string `json:"colour"`
		Regex  string `json:"regex"`
	} `json:"patterns"`
}

func parseLogColours(data []byte) ([]colourRule, error) {
	var config colourConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("invalid log colours: %w", err)
	}
	rules := make([]colourRule, 0, len(config.Patterns))
	for _, item := range config.Patterns {
		value, ok := config.Colours[item.Colour]
		if !ok || value == "" {
			return nil, fmt.Errorf("unknown or empty colour %q", item.Colour)
		}
		if item.Regex == "" {
			return nil, fmt.Errorf("empty regex for colour %q", item.Colour)
		}
		pattern, err := regexp.Compile(item.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex for colour %q: %w", item.Colour, err)
		}
		rules = append(rules, colourRule{pattern: pattern, colour: lipgloss.Color(value)})
	}
	return rules, nil
}

func colourLogLine(rules []colourRule, original, visible string) string {
	for _, rule := range rules {
		if rule.pattern.MatchString(original) {
			return lipgloss.NewStyle().Foreground(rule.colour).Render(visible)
		}
	}
	return visible
}
