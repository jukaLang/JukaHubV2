package main

// The disk pie poller used to statfs the card every five seconds for the whole
// session, whether or not its screen was up. These tests pin the gate that keeps
// hidden screens from polling and the scene-entry reading that replaces it.

import "testing"

func TestSceneHasPieChart(t *testing.T) {
	config := &Config{Scenes: []SceneConfig{
		{Name: "DiskSpace", Elements: []Element{
			{Type: "label", Text: "Card"},
			{Type: "piechart"},
		}},
		{Name: "Main", Elements: []Element{
			{Type: "button", Text: "Games", Trigger: "change_scene:Games"},
		}},
	}}

	prevIndex, prevConfig := currentSceneIndex, appConfig
	t.Cleanup(func() { currentSceneIndex, appConfig = prevIndex, prevConfig })
	appConfig = config

	currentSceneIndex = 0
	if !sceneHasPieChart(config) {
		t.Error("the Disk Space scene was not recognised as the chart's screen")
	}
	currentSceneIndex = 1
	if sceneHasPieChart(config) {
		t.Error("a scene without a pie chart was treated as the chart's screen")
	}

	currentSceneIndex = -1
	if sceneHasPieChart(config) {
		t.Error("no scene at all still polled the chart")
	}
	currentSceneIndex = 0
	if sceneHasPieChart(nil) {
		t.Error("a nil config still polled the chart")
	}
}

// TestSceneHasPieChartFollowsTheActiveSceneConfig reloads a config under the
// same scene index: the gate must read the config it is given, not a cached one.
func TestSceneHasPieChartFollowsTheActiveSceneConfig(t *testing.T) {
	prevIndex := currentSceneIndex
	t.Cleanup(func() { currentSceneIndex = prevIndex })
	currentSceneIndex = 0

	withChart := &Config{Scenes: []SceneConfig{{Name: "DiskSpace", Elements: []Element{{Type: "piechart"}}}}}
	withoutChart := &Config{Scenes: []SceneConfig{{Name: "Main", Elements: []Element{{Type: "label"}}}}}

	if !sceneHasPieChart(withChart) {
		t.Fatal("the chart scene was missed")
	}
	if sceneHasPieChart(withoutChart) {
		t.Fatal("the reloaded scene list was not read")
	}
}
