package main

import (
	"reflect"
	"testing"
)

// The registry lists a Smart Pro build and a Brick build of the same game as two
// entries. On a Brick the Brick one comes first, the Smart Pro one last, and a
// package for every device keeps its place among the first.
func TestPkgModelListsThisDevicesBuildsFirst(t *testing.T) {
	entries, err := pkgParseCatalog([]byte(`[
		{"name":"Blips (Smart Pro)","device":["trimui_smart_pro"],"download":"https://example.com/a.zip"},
		{"name":"Blips (Brick)","device":["trimui_brick"],"download":"https://example.com/b.zip"},
		{"name":"Calculator","device":["all"],"download":"https://example.com/c.zip"},
		{"name":"Zed","download":"https://example.com/z.zip"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	names := func(dev DeviceModel) []string {
		var out []string
		for _, it := range pkgBuildModel(pkgModelInput{Entries: entries, Device: dev}).Items {
			out = append(out, it.Pkg.Name)
		}
		return out
	}
	if got, want := names(DeviceTrimuiBrick), []string{"Blips (Brick)", "Calculator", "Zed", "Blips (Smart Pro)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("on a Brick: %v, want %v", got, want)
	}
	if got, want := names(DeviceTrimuiSmartPro), []string{"Blips (Smart Pro)", "Calculator", "Zed", "Blips (Brick)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("on a Smart Pro: %v, want %v", got, want)
	}
}
