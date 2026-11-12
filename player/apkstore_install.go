package main

// apkstore_install.go — a Store download becomes a launchable AndroidPak
// profile only after its bytes, package identity and native compatibility have
// been checked. The APK remains on the card; Games uses that file directly.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// apkStoreCheckInstall is kept separate from network and profile writes so the
// same compatibility gate is testable without a device or an APK repository.
func apkStoreCheckInstall(e apkStoreCatalogEntry, info apkStoreInfo) error {
	if !e.installable() || !apkStoreCatalogDownloadValid(e) {
		return errors.New("this store entry has no verified native APK to install")
	}
	if info.ManifestErr != "" || info.Manifest.Package == "" {
		return fmt.Errorf("the downloaded APK has no readable package identity: %s", info.ManifestErr)
	}
	if info.Manifest.Package != e.Package {
		return fmt.Errorf("the downloaded APK is %s, but the store entry promised %s", info.Manifest.Package, e.Package)
	}
	if info.Manifest.Split != "" || info.Manifest.SplitsRequired || info.Tier == apkTierSplit {
		return errors.New("the download is one piece of a split app; AndroidRT needs a complete APK")
	}
	if info.Manifest.Launcher != "android.app.NativeActivity" &&
		info.Manifest.Launcher != "com.google.androidgamesdk.GameActivity" {
		return errors.New("the APK does not launch a native Android activity; AndroidRT cannot start its Java activity")
	}
	if info.ChosenABI != "arm64-v8a" || !info.Tier.canAdd() {
		return fmt.Errorf("this APK cannot run on AndroidRT: %s", info.Verdict)
	}
	if strings.TrimSpace(info.EntryLib) == "" {
		return errors.New("the APK has no native entry library")
	}
	return nil
}

// apkStoreInstallCatalogAPK downloads an APK with the catalogue's SHA-256,
// inspects the actual package and creates the Games profile. A compatibility
// failure leaves the verified APK in Your APKs so the user can inspect it.
func apkStoreInstallCatalogAPK(ctx context.Context, e apkStoreCatalogEntry) (apkStoreInfo, GameProfile, error) {
	if !e.installable() || !apkStoreCatalogDownloadValid(e) {
		return apkStoreInfo{}, GameProfile{}, errors.New("this store entry has no verified native APK to install")
	}
	path, err := apkStoreDownload(ctx, e)
	if err != nil {
		return apkStoreInfo{}, GameProfile{}, err
	}
	info := apkStoreAnalyze(path, true)
	if err := apkStoreCheckInstall(e, info); err != nil {
		return info, GameProfile{}, err
	}
	p, err := apkStoreAddProfile(info)
	return info, p, err
}
