package main

// The Ports panel reads PortMaster's own catalogue. A selected port goes into
// the shared install queue (ports_store_install.go): verified against the
// catalogue's MD5, then installed by PortMaster's own installer, which fetches
// the runtimes the port declares. The first row opens the Ports Store.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var portCatalogLoadSerial uint64 // UI thread only; prevents a closed panel's result landing in a new one

// portsStoreRow heads the catalogue: the Ports Store is the full view of it
// (screenshots, genres, Ready to Run ports, one-press installs).
var portsStoreRow = portEntry{Name: "Open the Ports Store", Detail: "Screenshots, genres, Ready to Run ports and one-press installs", Info: true, Store: true}

func portsPanelRows() []portEntry {
	if portsPanel.opts {
		return portsPanel.optsRows
	}
	if !portsPanel.catalog {
		return portEntries()
	}
	if portsPanel.catalogLoading {
		return []portEntry{portsStoreRow, {Name: "Loading PortMaster's catalogue…", Detail: "Connect to Wi-Fi to browse ports", Info: true}}
	}
	if portsPanel.catalogErr != "" {
		return []portEntry{portsStoreRow, {Name: "Catalogue unavailable", Detail: portsPanel.catalogErr + " · A retries", Info: true}}
	}
	if len(portsPanel.catalogRows) == 0 {
		return []portEntry{portsStoreRow, {Name: "No matching ports", Detail: hintSentence2("Press %s to search or %s to change the May Run filter", "R1", "Search", "Y", "May Run"), Info: true}}
	}
	return portsPanel.catalogRows
}

func portsPanelToggleCatalog() {
	portsPanel.catalog = !portsPanel.catalog
	portsPanel.cursor, portsPanel.scroll = 0, 0
	if portsPanel.catalog && len(portsPanel.catalogEntries) == 0 && !portsPanel.catalogLoading {
		portsPanelLoadCatalog()
	}
	PlayNavSound()
}

func portsPanelToggleMayRun() {
	if !portsPanel.catalog {
		return
	}
	portsPanel.mayRun = !portsPanel.mayRun
	portsPanel.cursor, portsPanel.scroll = 0, 0
	portsPanelRebuildCatalogRows()
	PlayNavSound()
}

func portsPanelSetQuery(query string) {
	// The Ports Store reuses this keyboard (vkTargetPortsSearch); the panel
	// open over the store keeps its own search.
	if !portsPanel.open && portsStoreOnScreen(appConfig) {
		portsPanel.query = query
		pstoreSetQuery(query)
		return
	}
	if !portsPanel.open || !portsPanel.catalog {
		return
	}
	if query == portsPanel.query {
		return
	}
	portsPanel.query = query
	portsPanel.cursor, portsPanel.scroll = 0, 0
	portsPanelRebuildCatalogRows()
}

func portsPanelLoadCatalog() {
	if portsPanel.catalogLoading {
		return
	}
	portsPanel.catalogLoading = true
	portsPanel.catalogErr = ""
	portCatalogLoadSerial++
	gen := portCatalogLoadSerial
	portsPanel.catalogGeneration = gen
	go func() {
		entries, err := mcPortsCatalog(context.Background())
		tbxPost(func() {
			if !portsPanel.open || portsPanel.catalogGeneration != gen {
				return
			}
			portsPanel.catalogLoading = false
			if err != nil {
				portsPanel.catalogErr = firstLine(err.Error())
				if portsPanel.opts {
					portsPanelBuildOptions()
				}
				return
			}
			portsPanel.catalogEntries = entries
			portsPanelRebuildCatalogRows()
			// The installed rows learn which ports have a newer build, and
			// an open port's options get their Update row.
			invalidatePortsCache()
			if portsPanel.opts {
				portsPanelBuildOptions()
			}
		})
	}()
}

func portCatalogHostGlibc() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	class, machine := elfHostArch(runtime.GOARCH)
	dirs := elfLibrarySearchDirs(nil, os.Getenv("JUKAHUB_ORIG_LD_LIBRARY_PATH"), os.Getenv("LD_LIBRARY_PATH"))
	info, state := elfFindLibrary("libc.so.6", class, machine, dirs)
	if state != elfLibFound {
		return ""
	}
	return elfHighestInFamily(info.defs, "GLIBC")
}

// portCatalogMayRun removes only ports whose own metadata contradicts this
// device. A missing requirement or architecture is unknown, not a guarantee.
func portCatalogMayRun(e mcCatalogEntry, device DeviceModel, hostGlibc string) bool {
	if !mcCatalogArchOK(e.Arch) {
		return false
	}
	if e.MinGlibc != "" && hostGlibc != "" &&
		elfVersionLess(hostGlibc, "GLIBC_"+strings.TrimPrefix(e.MinGlibc, "GLIBC_")) &&
		!privateGlibcCatalogEligible(e, device) {
		return false
	}
	brick := device == DeviceTrimuiBrick || device == DeviceTrimuiHammer
	for _, requirement := range e.Requirements {
		switch strings.ToLower(strings.TrimSpace(requirement)) {
		case "!trimui":
			if device.IsTrimui() {
				return false
			}
		case "analog_1", "analog_2", "analog_1|analog_2":
			if brick {
				return false
			}
		case "4:3":
			if device == DeviceTrimuiSmartPro || device == DeviceTrimuiSmartProS {
				return false
			}
		case "!4:3":
			if brick {
				return false
			}
		case "opengl":
			if device.IsTrimui() {
				return false // the handheld exposes GLES, not desktop OpenGL
			}
		}
	}
	return true
}

func portCatalogInstalledNames() map[string]bool {
	names := map[string]bool{}
	for _, p := range scanPortLaunchers(portRoots(portMasterSDRoot)) {
		names[strings.ToLower(p.Name)] = true
	}
	return names
}

func portCatalogIsInstalled(e mcCatalogEntry, names map[string]bool) bool {
	if names[strings.ToLower(strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip)))] {
		return true
	}
	for _, name := range e.Items {
		if strings.EqualFold(filepath.Ext(name), ".sh") &&
			names[strings.ToLower(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))] {
			return true
		}
	}
	return false
}

func portCatalogRow(e mcCatalogEntry, installed bool) portEntry {
	title := e.Title
	if title == "" {
		title = strings.TrimSuffix(e.Zip, filepath.Ext(e.Zip))
	}
	// The installation note tells players when a port needs files they own;
	// that matters more at this point than the catalogue synopsis.
	detail := e.Instructions
	if detail == "" {
		detail = e.Description
	}
	if detail == "" {
		detail = "PortMaster installs the port and its declared runtimes"
	}
	status := emuMissing
	if installed {
		status = emuAvailable
	}
	return portEntry{Name: title, Detail: detail, Status: status, Catalog: e}
}

func portsPanelRebuildCatalogRows() {
	if len(portsPanel.catalogEntries) == 0 {
		portsPanel.catalogRows = nil
		return
	}
	device, glibc := DetectDevice(), portCatalogHostGlibc()
	names := portCatalogInstalledNames()
	query := strings.ToLower(strings.Join(strings.Fields(portsPanel.query), " "))
	rows := make([]portEntry, 0, len(portsPanel.catalogEntries))
	for _, entry := range portsPanel.catalogEntries {
		if portsPanel.mayRun && !portCatalogMayRun(entry, device, glibc) {
			continue
		}
		title := entry.Title
		if title == "" {
			title = entry.Zip
		}
		if query != "" && !strings.Contains(strings.ToLower(title+" "+entry.Zip+" "+entry.Description), query) {
			continue
		}
		rows = append(rows, portCatalogRow(entry, portCatalogIsInstalled(entry, names)))
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	if len(rows) == 0 {
		rows = append(rows, portEntry{Name: "No matching ports", Detail: hintSentence2("Press %s to search or %s to change the May Run filter", "R1", "Search", "Y", "May Run"), Info: true})
	}
	portsPanel.catalogRows = append([]portEntry{portsStoreRow}, rows...)
}

func portCatalogSummary() string {
	if portsPanel.catalogLoading {
		return "Reading PortMaster's live catalogue"
	}
	if portsPanel.catalogErr != "" {
		return "Connect to Wi-Fi and press A to retry"
	}
	filter := "All ports"
	if portsPanel.mayRun {
		filter = "May Run Here"
	}
	if portsPanel.query != "" {
		filter += " · " + strings.TrimSpace(portsPanel.query)
	}
	shown := 0
	for _, r := range portsPanel.catalogRows {
		if r.Catalog.Zip != "" {
			shown++
		}
	}
	return fmt.Sprintf("%s · %d shown", filter, shown)
}

// portCatalogInstallStart queues a catalogue port in the shared install
// queue (ports_store_install.go), which downloads and checks it and lets
// PortMaster install it — headless when it can, else by opening PortMaster.
func portCatalogInstallStart(e mcCatalogEntry) error {
	if runtime.GOOS != "linux" {
		return errors.New("ports install on the handheld")
	}
	if err := pstoreEnqueueWith(e, true); err != nil {
		return err
	}
	n, _ := pstoreQueueCounts()
	msg := pstoreTitle(e) + " is downloading — the status strip shows how far"
	if n > 1 {
		msg = pstoreTitle(e) + " is queued (" + pluralizeCount(n, "port", "ports") + " in the queue)"
	}
	ShowToast(msg, ToastKindInfo)
	return nil
}

func portCatalogDownload(ctx context.Context, gen uint64, e mcCatalogEntry, inbox string) (string, error) {
	if e.Zip == "" || filepath.Base(e.Zip) != e.Zip || !strings.EqualFold(filepath.Ext(e.Zip), ".zip") {
		return "", errors.New("invalid port archive name")
	}
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return "", err
	}
	final := filepath.Join(inbox, e.Zip)
	if regularFile(final) {
		if e.MD5 != "" {
			sum, err := mcFileMD5(final)
			if err != nil || !strings.EqualFold(sum, e.MD5) {
				return "", errors.New("a different copy is already queued in PortMaster")
			}
		}
		return final, nil
	}
	part := filepath.Join(inbox, "."+e.Zip+".part")
	defer os.Remove(part)
	installBarUpdateRun(gen, installPhaseDownload, 0, e.Size)
	if err := downloadFileWithProgressCtx(ctx, e.URL, part, func(done, total int64) {
		installBarUpdateRun(gen, installPhaseDownload, done, total)
	}); err != nil {
		return "", err
	}
	if e.MD5 != "" {
		installBarUpdateRun(gen, installPhaseVerify, 0, 0)
		sum, err := mcFileMD5(part)
		if err != nil || !strings.EqualFold(sum, e.MD5) {
			return "", fmt.Errorf("%s failed PortMaster's checksum", e.Zip)
		}
	}
	if err := mcSyncFile(part); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	return final, nil
}
