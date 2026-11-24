package main

import "testing"

// The parsers read what any device on the network sends: they must take a
// real server's answers and refuse addresses off the home network.
func TestDLNAParsers(t *testing.T) {
	reply := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nLOCATION: http://192.168.1.20:8200/rootDesc.xml\r\n" +
		"ST: urn:schemas-upnp-org:device:MediaServer:1\r\nUSN: uuid:4d696e69::urn:schemas-upnp-org:device:MediaServer:1\r\n\r\n"
	if loc, ok := dlnaParseSearchReply([]byte(reply)); !ok || loc != "http://192.168.1.20:8200/rootDesc.xml" {
		t.Fatalf("search reply: %q %v", loc, ok)
	}
	for _, bad := range []string{"http://203.0.113.9/desc.xml", "http://127.0.0.1:8200/d.xml", "ftp://192.168.1.20/d.xml"} {
		if _, ok := dlnaLANAddress(bad); ok {
			t.Errorf("%s accepted as a home-network address", bad)
		}
	}

	desc := `<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device>` +
		`<deviceType>urn:schemas-upnp-org:device:Basic:1</deviceType><friendlyName>NAS</friendlyName><UDN>uuid:root</UDN>` +
		`<deviceList><device><deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>` +
		`<friendlyName>Living room NAS</friendlyName><UDN>uuid:media</UDN>` +
		`<iconList><icon><mimetype>image/png</mimetype><width> 48 </width><height>48</height><url>/icons/sm.png</url></icon>` +
		`<icon><mimetype>image/png</mimetype><width>120</width><height>120</height><url>/icons/lrg.png</url></icon></iconList>` +
		`<serviceList><service><serviceType>urn:schemas-upnp-org:service:ContentDirectory:1</serviceType>` +
		`<controlURL>/ctl/ContentDir</controlURL></service></serviceList></device></deviceList></device></root>`
	s, ok := dlnaParseDescription([]byte(desc), "http://192.168.1.20:8200/rootDesc.xml")
	if !ok || s.Name != "Living room NAS" || s.UDN != "uuid:media" || s.ControlURL != "http://192.168.1.20:8200/ctl/ContentDir" ||
		s.IconURL != "http://192.168.1.20:8200/icons/lrg.png" {
		t.Fatalf("description: %+v %v", s, ok)
	}

	didl := `<DIDL-Lite xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/" xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/">` +
		`<container id="64" parentID="0" childCount="3"><dc:title>Films &amp; TV</dc:title>` +
		`<upnp:class>object.container.storageFolder</upnp:class></container>` +
		`<item id="64$1" parentID="64"><dc:title>Night of the Living Dead</dc:title><dc:date>1968-10-01</dc:date>` +
		`<upnp:class>object.item.videoItem</upnp:class>` +
		`<res protocolInfo="http-get:*:video/x-matroska:*" size="2000000" duration="1:36:04.000" resolution="1920x1080">http://192.168.1.20:8200/MediaItems/1.mkv</res>` +
		`<res protocolInfo="http-get:*:video/mp4:*" resolution="1280x720">http://192.168.1.20:8200/MediaItems/1.mp4</res>` +
		`<res protocolInfo="http-get:*:image/jpeg:DLNA.ORG_PN=JPEG_TN">http://192.168.1.20:8200/Thumbs/1.jpg</res></item>` +
		`<item id="64$2"><dc:title>Song</dc:title><upnp:class>object.item.audioItem.musicTrack</upnp:class>` +
		`<res protocolInfo="http-get:*:audio/mpeg:*">http://192.168.1.20:8200/MediaItems/2.mp3</res></item>` +
		`<item id="64$3"><dc:title>Elsewhere</dc:title><upnp:class>object.item.videoItem</upnp:class>` +
		`<res protocolInfo="http-get:*:video/mp4:*">http://198.51.100.7/film.mp4</res></item></DIDL-Lite>`
	env := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:BrowseResponse xmlns:u="urn:schemas-upnp-org:service:ContentDirectory:1"><Result>` +
		xmlEscapeForTest(didl) + `</Result><NumberReturned>4</NumberReturned><TotalMatches>4</TotalMatches>` +
		`<UpdateID>1</UpdateID></u:BrowseResponse></s:Body></s:Envelope>`
	result, n, total, err := dlnaParseBrowse([]byte(env), 200)
	if err != nil || n != 4 || total != 4 {
		t.Fatalf("browse: %v %d %d", err, n, total)
	}
	folders, videos := dlnaParseDIDL(result)
	if len(folders) != 1 || folders[0].Title != "Films & TV" || folders[0].Count != 3 {
		t.Fatalf("folders: %+v", folders)
	}
	if len(videos) != 1 {
		t.Fatalf("videos (the song and the internet address must be left out): %+v", videos)
	}
	v := videos[0]
	if v.URL != "http://192.168.1.20:8200/MediaItems/1.mp4" || v.Year != 1968 || v.Art != "http://192.168.1.20:8200/Thumbs/1.jpg" {
		t.Fatalf("video: %+v", v)
	}
	if got := dlnaParseDuration("1:36:04.000"); got != 5764 {
		t.Errorf("duration = %v", got)
	}

	fault := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultstring>UPnPError</faultstring>` +
		`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>701</errorCode>` +
		`<errorDescription>No such object</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`
	if _, _, _, err := dlnaParseBrowse([]byte(fault), 500); err == nil || err.Error() != "the server refused: No such object" {
		t.Errorf("fault: %v", err)
	}
}

func xmlEscapeForTest(s string) string {
	r := []rune{}
	for _, c := range s {
		switch c {
		case '<':
			r = append(r, []rune("&lt;")...)
		case '>':
			r = append(r, []rune("&gt;")...)
		case '&':
			r = append(r, []rune("&amp;")...)
		case '"':
			r = append(r, []rune("&quot;")...)
		default:
			r = append(r, c)
		}
	}
	return string(r)
}
