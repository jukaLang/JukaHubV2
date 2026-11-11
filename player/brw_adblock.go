package main

// brw_adblock.go — the second half of the reader's ad and tracker filter.
//
// adblock.go has always kept requests to a short list of advertising hosts off
// the wire. WPE Browser on the same handheld showed what a filter is worth when
// it is broad: its EasyList/EasyPrivacy pass makes pages lighter and faster on
// handheld Wi-Fi. EasyList itself cannot be shipped here (its licence is not
// this project's, and its tens of thousands of cosmetic rules mean nothing to a
// reader), so this file carries JukaHub's own curated list of the ad,
// measurement and tracking hosts that turn up most, written for a reader: host
// suffixes only, matched on dot boundaries, nothing that serves a page's own
// content (no CDNs, no font or library hosts, no video hosts).
//
// It also adds three rules the starter list did not have:
//
//   - a switch, Settings → Browser → Block ads and trackers (default on), so a
//     reader who wants every request made can have that everywhere at once;
//   - the first-party rule: a listed host is only blocked on someone else's
//     page. Reading criteo.com's own site loads criteo.com's own pictures; an
//     ad network's script on a news site is still dropped. URL-path hints
//     ("/ads/", "/pixel.gif") still apply on a site's own pages: an /ads/
//     folder is an ad on any site;
//   - the per-site exception is reachable from Site info as "Allow ads on this
//     site", next to the page's count ("14 ads and trackers blocked"). It is
//     the same exception the Filter chip sets (data/browser-block.json), so
//     there is one list of excepted sites, not two.
//
// The top-level page the user opened, and every link they follow, never pass
// through the filter: only a page's subresources do (scripts, fetch/XHR,
// stylesheets, images — the reader never loads iframes).

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// browserBlockAdsKey is the Custom key of Settings → Browser → Block ads and
// trackers.
const browserBlockAdsKey = "browserBlockAds"

// browserBlockAdsOff caches that setting for the page-load goroutines, which
// must not read the config's Custom map while the main thread may be writing
// it — the same arrangement as browserJavaScriptOff. It is refreshed on the
// main thread when a page load starts (browserLoadURL) and when Settings
// changes the value; before the first load the filter is on, the default.
var browserBlockAdsOff atomic.Bool

// browserBlockAdsSetting reads the setting from a config, in any of the shapes
// a stored switch arrives in (browserJavaScriptSetting reads the same way).
// Anything it does not recognise leaves the filter on. Main thread only.
func browserBlockAdsSetting(c *Config) bool {
	if c == nil || c.Variables.Custom == nil {
		return true
	}
	v, ok := customValue(c.Variables.Custom, browserBlockAdsKey)
	if !ok {
		return true
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "false", "0", "no", "off":
			return false
		}
	case float64:
		return t != 0
	}
	return true
}

// refreshBrowserBlockAdsSetting copies the setting into the cache the fetch
// goroutines read. Main thread only.
func refreshBrowserBlockAdsSetting(c *Config) {
	browserBlockAdsOff.Store(!browserBlockAdsSetting(c))
}

// browserBlockAdsEnabled reports whether the filter is on at all. Safe from any
// goroutine.
func browserBlockAdsEnabled() bool { return !browserBlockAdsOff.Load() }

// brwAdBlockSettingsRow is Settings → Browser → Block ads and trackers. The
// page cache is dropped on a change: Back would otherwise show a page filtered
// under the old rule.
func brwAdBlockSettingsRow() setRow {
	return setRow{
		ID: "blockAds", Label: "Block ads and trackers", Kind: setKindToggle,
		Detail: setStatic("Skip ad and tracker requests on pages · Site info allows them per site"),
		Get:    browserBlockAdsSetting,
		Set: func(c *Config, on bool) {
			setStoreBool(c, browserBlockAdsKey, on)
			refreshBrowserBlockAdsSetting(c)
			browserClearPageCache()
			setToast("Block ads and trackers: " + setOnOff(on))
		},
	}
}

// browserAdBlockSiteAllowed reports whether the user excepted the site a URL
// belongs to (Allow ads on this site, or the Filter chip), whatever the
// switch says. browserAdBlockOff is this plus the switch.
func browserAdBlockSiteAllowed(raw string) bool {
	site := browserAdBlockSiteKey(raw)
	if site == "" {
		return false
	}
	browserAdBlockSites.mu.Lock()
	defer browserAdBlockSites.mu.Unlock()
	ensureBrowserAdBlockSitesLocked()
	return browserAdBlockSites.disabled[site]
}

// browserAdHostsMore extends browserAdHosts (adblock.go). Every entry is a host
// suffix: "example.net" matches example.net and every subdomain of it, never
// notexample.net. Grouped by what the hosts do; the test keeps the list free
// of duplicates and of entries an existing one already covers.
var browserAdHostsMore = []string{
	// Google and Microsoft advertising, measurement and experiments
	"imasdk.googleapis.com",
	"fundingchoicesmessages.google.com",
	"googleoptimize.com",
	"app-measurement.com",
	"admob.com",
	"atdmt.com",
	"msads.net",
	// Social networks' pixels and ad endpoints
	"an.facebook.com",
	"pixel.facebook.com",
	"ads-twitter.com",
	"ct.pinterest.com",
	"trk.pinterest.com",
	"log.pinterest.com",
	"events.redditmedia.com",
	"alb.reddit.com",
	"events.reddit.com",
	"sc-static.net",
	"tr.snapchat.com",
	"q.quora.com",
	// Ad exchanges, SSPs, DSPs and ad servers
	"adsafeprotected.com",
	"iasds01.com",
	"doubleverify.com",
	"moatads.com",
	"moatpixel.com",
	"3lift.com",
	"triplelift.com",
	"indexexchange.com",
	"lijit.com",
	"sovrn.com",
	"gumgum.com",
	"media.net",
	"contextweb.com",
	"districtm.io",
	"33across.com",
	"yieldlab.net",
	"improvedigital.com",
	"adition.com",
	"emxdgt.com",
	"connatix.com",
	"primis.tech",
	"springserve.com",
	"spotx.tv",
	"freewheel.tv",
	"stickyadstv.com",
	"tremorhub.com",
	"tribalfusion.com",
	"exponential.com",
	"adtechus.com",
	"advertising.com",
	"adkernel.com",
	"adhigh.net",
	"adzerk.net",
	"adbutler.com",
	"adpushup.com",
	"admixer.net",
	"adriver.ru",
	"adfox.ru",
	"betweendigital.com",
	"adnxs-simple.com",
	"turn.com",
	"mathtag.com",
	"bidr.io",
	"simpli.fi",
	"sitescout.com",
	"steelhousemedia.com",
	"dotomi.com",
	"serving-sys.com",
	"flashtalking.com",
	"innovid.com",
	"adtelligent.com",
	"smaato.net",
	"rfihub.com",
	"perfectaudience.com",
	"sascdn.com",
	"onetag-sys.com",
	"seedtag.com",
	"richaudience.com",
	"rhythmone.com",
	"1rx.io",
	"undertone.com",
	"unrulymedia.com",
	"vidazoo.com",
	"aniview.com",
	"kargo.com",
	"sonobi.com",
	"lkqd.net",
	"smartclip.net",
	"adformdsp.net",
	"rtbhouse.com",
	"creativecdn.com",
	"adhese.com",
	"adocean.pl",
	"adscale.de",
	"adspirit.de",
	"smartstream.tv",
	"adnami.io",
	"yieldmanager.com",
	"ads.yahoo.com",
	"gemini.yahoo.com",
	"assoc-amazon.com",
	"adswizz.com",
	"adlightning.com",
	"confiant-integrations.net",
	"pagefair.com",
	"pagefair.net",
	"getadmiral.com",
	"id5-sync.com",
	// Native ads, content recommendation and publisher ad platforms
	"taboolasyndication.com",
	"outbrainimg.com",
	"zemanta.com",
	"content.ad",
	"nativo.com",
	"ntv.io",
	"adsnative.com",
	"zergnet.com",
	"lentainform.com",
	"marketgid.com",
	"adskeeper.co.uk",
	"ligatus.com",
	"plista.com",
	"yieldlove.com",
	"adthrive.com",
	"mediavine.com",
	"ezoic.net",
	"ezojs.com",
	"pub.network",
	"buysellads.com",
	"buysellads.net",
	"servedby-buysellads.com",
	"carbonads.com",
	"carbonads.net",
	"ethicalads.io",
	"disqusads.com",
	"bidvertiser.com",
	"adblade.com",
	"infolinks.com",
	"vdo.ai",
	"colombiaonline.com",
	// Pop-under and aggressive networks
	"popads.net",
	"popcash.net",
	"propellerads.com",
	"propellerclick.com",
	"adsterra.com",
	"exoclick.com",
	"exosrv.com",
	"juicyads.com",
	"trafficjunky.net",
	"trafficstars.com",
	"hilltopads.net",
	"clickadu.com",
	"onclickads.net",
	"adcash.com",
	"revenuehits.com",
	// Affiliate tracking
	"skimresources.com",
	"skimlinks.com",
	"viglink.com",
	"redirectingat.com",
	"awin1.com",
	"zanox.com",
	"linksynergy.com",
	"anrdoezrs.net",
	"dpbolvw.net",
	"jdoqocy.com",
	"tkqlhce.com",
	"kqzyfj.com",
	"emjcd.com",
	"impactradius-event.com",
	"shareasale-analytics.com",
	"admitad.com",
	// Data brokers, identity graphs and audience platforms
	"rlcdn.com",
	"pippio.com",
	"agkn.com",
	"bluekai.com",
	"bkrtx.com",
	"addthis.com",
	"addthisedge.com",
	"sharethis.com",
	"krxd.net",
	"exelator.com",
	"demdex.net",
	"everesttech.net",
	"crwdcntrl.net",
	"tapad.com",
	"eyeota.net",
	"adsymptotic.com",
	"permutive.com",
	"permutive.app",
	"lytics.io",
	"blueconic.net",
	"mparticle.com",
	"cxense.com",
	"navdmp.com",
	"nuggad.net",
	"emetriq.de",
	"theadex.com",
	"bounceexchange.com",
	"bouncex.net",
	// Mobile ad and attribution SDKs
	"supersonicads.com",
	"applvn.com",
	"inmobi.com",
	"mopub.com",
	"tapjoy.com",
	"mintegral.com",
	"rayjump.com",
	"pangle.io",
	"pangolin-sdk-toutiao.com",
	"liftoff.io",
	"appsflyer.com",
	"adjust.com",
	"kochava.com",
	"singular.net",
	// Analytics, session replay, A/B testing and audience measurement
	"omtrdc.net",
	"2o7.net",
	"adobedtm.com",
	"tealiumiq.com",
	"chartbeat.com",
	"chartbeat.net",
	"parsely.com",
	"quantcount.com",
	"quantcast.com",
	"imrworldwide.com",
	"voicefive.com",
	"statcounter.com",
	"histats.com",
	"woopra.com",
	"kissmetrics.com",
	"kissmetrics.io",
	"crazyegg.com",
	"mouseflow.com",
	"luckyorange.com",
	"luckyorange.net",
	"inspectlet.com",
	"smartlook.com",
	"logrocket.io",
	"lr-ingest.io",
	"quantummetric.com",
	"contentsquare.net",
	"decibelinsight.net",
	"glassboxdigital.io",
	"sessioncam.com",
	"clicktale.net",
	"optimizely.com",
	"abtasty.com",
	"visualwebsiteoptimizer.com",
	"pendo.io",
	"heapanalytics.com",
	"hotjar.io",
	"getclicky.com",
	"gaug.es",
	"plausible.io",
	"simpleanalyticscdn.com",
	"usefathom.com",
	"cloudflareinsights.com",
	"akstat.io",
	"go-mpulse.net",
	"browser-intake-datadoghq.com",
	"browser-intake-datadoghq.eu",
	"jwpltx.com",
	// Marketing automation trackers (the tracking hosts, not the products' sites)
	"hs-analytics.net",
	"hs-scripts.com",
	"hscollectedforms.net",
	"hsadspixel.net",
	"munchkin.marketo.net",
	"pi.pardot.com",
	"en25.com",
	"bizible.com",
	"6sc.co",
	"tag.demandbase.com",
	"clearbitscripts.com",
	"lfeeder.com",
	"ws.zoominfo.com",
	// European audience measurement
	"ioam.de",
	"xiti.com",
	"ati-host.net",
	"webtrekk.net",
	"wt-safetag.com",
	"etracker.com",
	"econda-monitor.de",
	"gemius.pl",
	"uimserv.net",
	// Russian, Chinese, Japanese and Korean ad and analytics networks
	"mc.yandex.ru",
	"mc.yandex.com",
	"an.yandex.ru",
	"yabs.yandex.ru",
	"yandexadexchange.net",
	"top-fwz1.mail.ru",
	"ad.mail.ru",
	"counter.rambler.ru",
	"counter.yadro.ru",
	"tns-counter.ru",
	"hm.baidu.com",
	"pos.baidu.com",
	"cpro.baidu.com",
	"cnzz.com",
	"umeng.com",
	"mmstat.com",
	"tanx.com",
	"irs01.com",
	"miaozhen.com",
	"admaster.com.cn",
	"gridsum.com",
	"i-mobile.co.jp",
	"microad.jp",
	"adingo.jp",
	"ad-stir.com",
	"nend.net",
	"impact-ad.jp",
	"logly.co.jp",
	"gssprt.jp",
	"wcs.naver.net",
}

// browserAdHostSet holds every host-only suffix of both lists, so a lookup
// walks the host's own labels instead of the whole list. Entries with a path
// (facebook.com/tr) stay with adblock.go's loop, which checks the path too.
var browserAdHostSet = func() map[string]bool {
	set := make(map[string]bool, len(browserAdHosts)+len(browserAdHostsMore))
	for _, list := range [][]string{browserAdHosts, browserAdHostsMore} {
		for _, entry := range list {
			if entry = strings.ToLower(strings.TrimSpace(entry)); entry != "" && !strings.Contains(entry, "/") {
				set[entry] = true
			}
		}
	}
	return set
}()

// browserAdHostListed reports whether host is a listed ad or tracker host or a
// subdomain of one. It steps through the host's own suffixes, so
// "ads.doubleclick.net" finds "doubleclick.net" and "notdoubleclick.net"
// finds nothing.
func browserAdHostListed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for host != "" {
		if browserAdHostSet[host] {
			return true
		}
		dot := strings.IndexByte(host, '.')
		if dot < 0 {
			return false
		}
		host = host[dot+1:]
	}
	return false
}

// brwRegistrableDomain is the site a host belongs to: its public suffix and
// one label more (news.example.co.uk → example.co.uk), from the jar's
// built-in suffix list (browserpsl.go). An IP address or a single-label name
// is its own site.
func brwRegistrableDomain(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return host
	}
	suffix := browserPublicSuffixes{}.PublicSuffix(host)
	if host == suffix {
		return host
	}
	rest := strings.TrimSuffix(host, "."+suffix)
	if dot := strings.LastIndexByte(rest, '.'); dot >= 0 {
		rest = rest[dot+1:]
	}
	return rest + "." + suffix
}

// brwAdSameSite reports whether host belongs to the same site as the page at
// pageURL. An unknown page is nobody's site.
func brwAdSameSite(pageURL, host string) bool {
	page, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil || page.Hostname() == "" || strings.TrimSpace(host) == "" {
		return false
	}
	return brwRegistrableDomain(page.Hostname()) == brwRegistrableDomain(host)
}

// brwAdBlocks is the whole verdict for one subresource of the page at pageURL:
// the lists say it is an ad or a tracker, and it is not the page's own site
// asking for its own files. On its own site a listed host only loses requests
// whose path is an ad path. It neither counts nor consults the switches; the
// callers in adblock.go do both.
func brwAdBlocks(pageURL, rawURL string) bool {
	if !browserURLIsAd(rawURL) {
		return false
	}
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return true
	}
	host := target.Hostname()
	if host == "" || !brwAdSameSite(pageURL, host) {
		return true
	}
	// First party: ask again with the host taken away, so only the path hints
	// can still say no.
	return browserURLIsAd((&url.URL{Path: target.Path, RawQuery: target.RawQuery}).String())
}

// brwAdBlockedLine is Site info's sentence about the page's ads and trackers.
func brwAdBlockedLine(blocked int, siteAllowed, filterOn bool) string {
	switch {
	case !filterOn:
		return "Ads and trackers are not blocked · Settings → Browser"
	case siteAllowed:
		return "Ads and trackers allowed on this site"
	case blocked <= 0:
		return "No ads or trackers found on this page"
	case blocked == 1:
		return "1 ad or tracker blocked"
	}
	return strconv.Itoa(blocked) + " ads and trackers blocked"
}

// ── Site info ────────────────────────────────────────────────────────────

// brwInfoAdsState is what Site info says about the page's ads: how many
// requests its load skipped, whether its site is excepted, and whether the
// filter is on at all. The count is the toolbar's, kept only while it belongs
// to the sheet's site.
func brwInfoAdsState(config *Config) (blocked int, allowed, on bool) {
	on = browserBlockAdsEnabled()
	allowed = browserAdBlockSiteAllowed(brwInfo.url)
	if elem, ok := findBrowserElement(config); ok {
		if view := browserAdBlockViewFor(elem.Variable); view.Site == browserAdBlockSiteKey(brwInfo.url) {
			blocked = view.Blocked
		}
	}
	return blocked, allowed, on
}

// brwInfoAdsHint is the sheet's hint for X — allow or block ads on this site —
// and nothing while the filter is off.
func brwInfoAdsHint() FooterHint {
	if !brwInfo.open || !browserBlockAdsEnabled() {
		return FooterHint{}
	}
	if browserAdBlockSiteAllowed(brwInfo.url) {
		return FooterHint{Button: "X", Label: "Block ads"}
	}
	return FooterHint{Button: "X", Label: "Allow ads"}
}

// brwInfoDrawAds draws the sheet's ads line from y — a shield and the count
// ("14 ads and trackers blocked"), and at the right the per-site switch with
// its X badge — and returns the y under it. The line is on every tab: it is
// about the page, not about one report.
func brwInfoDrawAds(r *sdl.Renderer, config *Config, card sdl.Rect, y int32, small, body *ttf.Font) int32 {
	brwInfo.ads = sdl.Rect{}
	if r == nil || small == nil || body == nil {
		return y
	}
	blocked, allowed, on := brwInfoAdsState(config)
	tone := brwToneNeutral
	switch {
	case on && allowed:
		tone = brwToneWarn
	case on && blocked > 0:
		tone = brwToneGood
	}
	h := int32(body.Height()) + 12
	x := card.X + OverlayPadX
	right := card.X + card.W - OverlayPadX
	if on {
		label := "Allow ads on this site"
		if allowed {
			label = "Block ads on this site"
		}
		key := hintKeyFor("X", label)
		lw, lh := measureText(small, label)
		kw, _ := measureText(small, key)
		badgeW, badgeH := kw+12, lh+6
		button := sdl.Rect{W: 10 + badgeW + 10 + lw + 16, H: h}
		button.X, button.Y = right-button.W, y
		drawGlassSurface(r, button, button.H/2, glassState{})
		badge := sdl.Rect{X: button.X + 10, Y: button.Y + (h-badgeH)/2, W: badgeW, H: badgeH}
		fillRoundedRect(r, badge.X, badge.Y, badge.W, badge.H, badge.H/2, glassChipFill())
		strokeRoundedRect(r, badge.X, badge.Y, badge.W, badge.H, badge.H/2, 1, glassChipEdge())
		renderText(r, config, small, key, glassChipInk(), badge.X+6, badge.Y+3)
		renderText(r, config, small, label, ColorTextSecondary(), badge.X+badge.W+10, button.Y+(h-lh)/2)
		brwInfo.ads = button
		right = button.X - 12
	}
	ink := brwToneColor(tone)
	icon := sdl.Rect{X: x, Y: y + (h-18)/2, W: 18, H: 18}
	drawHomeGlyphTinted(r, "shield", icon, ink)
	textX := icon.X + icon.W + 10
	if avail := right - textX; avail > 0 {
		line := ellipsize(body, brwAdBlockedLine(blocked, allowed, on), avail)
		renderText(r, config, body, line, ink, textX, y+(h-int32(body.Height()))/2)
	}
	return y + h + 10
}

// brwInfoToggleAds is X on the sheet (and a click on its switch): the site's
// exception flips and the page reloads under it, through the same path as the
// Filter chip. The sheet closes first, so the reloaded page is what shows.
func brwInfoToggleAds(config *Config) {
	if !brwInfo.open {
		return
	}
	if !browserBlockAdsEnabled() {
		PlayNavSound()
		ShowToast("Blocking ads and trackers is off · Settings → Browser turns it on", ToastKindInfo)
		return
	}
	brwInfoForget()
	browserToggleAdFilter(config)
}
