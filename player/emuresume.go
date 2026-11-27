package main

// emuresume.go — the pure decisions behind resuming a cut-off emulator
// download (DESK-12). No network or SDL here so the edge cases are testable.

import "strconv"

// emuResumeOffset is the byte offset to ask for from a leftover .part. A
// missing, empty or oversized part starts from zero; a plausible one is
// resumed. maxBytes <= 0 means "no cap".
func emuResumeOffset(partSize, maxBytes int64) int64 {
	if partSize <= 0 {
		return 0
	}
	if maxBytes > 0 && partSize > maxBytes {
		return 0
	}
	return partSize
}

// emuRangeAction is what the server's answer to a Range request means.
type emuRangeAction uint8

const (
	emuRangeFail emuRangeAction = iota // an unexpected status: do not write
	emuRangeRestart                    // 200: the server ignored Range, restart
	emuRangeAppend                     // 206: append to the .part
	emuRangeDropRetry                  // 416: the part is stale, drop it and retry
)

func emuRangeActionFor(status int, offset int64) emuRangeAction {
	if offset <= 0 {
		if status == 200 {
			return emuRangeRestart
		}
		return emuRangeFail
	}
	switch status {
	case 200:
		return emuRangeRestart
	case 206:
		return emuRangeAppend
	case 416:
		return emuRangeDropRetry
	}
	return emuRangeFail
}

// emuParseContentRange reads `bytes <start>-<end>/<total>` from a 206. A
// missing or inconsistent header is treated as unknown rather than trusted:
// the caller can still finish the transfer, it just cannot show a total.
func emuParseContentRange(header string, offset int64) (total int64, ok bool) {
	if len(header) < 6 || header[:6] != "bytes " {
		return 0, false
	}
	span := header[6:]
	slash := -1
	for i := 0; i < len(span); i++ {
		if span[i] == '/' {
			slash = i
			break
		}
	}
	if slash < 0 {
		return 0, false
	}
	dash := -1
	for i := 0; i < slash; i++ {
		if span[i] == '-' {
			dash = i
			break
		}
	}
	if dash < 0 {
		return 0, false
	}
	start, err1 := strconv.ParseInt(span[:dash], 10, 64)
	end, err2 := strconv.ParseInt(span[dash+1:slash], 10, 64)
	total, err3 := strconv.ParseInt(span[slash+1:], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start != offset || end < start || total <= end {
		return 0, false
	}
	return total, true
}
