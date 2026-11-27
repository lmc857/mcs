package sipserver

import (
	"fmt"
	"strconv"
	"strings"
)

type sdpInfo struct {
	AudioPort     int
	VideoPort     int
	ConnIP        string
	PayloadType   uint8
	Codec         string
	VideoPT       uint8
	VideoCodec    string
	AudioProto    string
	VideoProto    string
	AudioSecure   bool
	VideoSecure   bool
	AudioRTCPPort int
	VideoRTCPPort int
	AudioRTCPMux  bool
	VideoRTCPMux  bool
	AudioCrypto   cryptoInfo
	VideoCrypto   cryptoInfo
	HasAudio      bool
	HasVideo      bool
	AudioPayloads []uint8
	AudioCodecs   map[uint8]string
}

func parseSDP(body []byte) sdpInfo {
	info := sdpInfo{AudioCodecs: map[uint8]string{}}
	media := ""
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "c=IN IP4 "):
			info.ConnIP = strings.TrimSpace(strings.TrimPrefix(line, "c=IN IP4 "))
		case strings.HasPrefix(line, "m=video "):
			media = "video"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if p, err := strconv.Atoi(fields[1]); err == nil {
					info.VideoPort = p
				}
			}
			if len(fields) >= 4 {
				if pt, err := strconv.Atoi(fields[3]); err == nil {
					info.VideoPT = uint8(pt)
				}
			}
			if len(fields) >= 3 {
				info.VideoProto = strings.ToUpper(fields[2])
				info.VideoSecure = strings.Contains(info.VideoProto, "SAVP")
			}
			info.VideoCodec = ""
			info.HasVideo = true
		case strings.HasPrefix(line, "m=audio "):
			media = "audio"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if p, err := strconv.Atoi(fields[1]); err == nil {
					info.AudioPort = p
				}
			}
			if len(fields) >= 4 {
				if pt, err := strconv.Atoi(fields[3]); err == nil {
					info.PayloadType = uint8(pt)
				}
			}
			if len(fields) >= 3 {
				info.AudioProto = strings.ToUpper(fields[2])
				info.AudioSecure = strings.Contains(info.AudioProto, "SAVP")
			}
			for _, f := range fields[3:] {
				if pt, err := strconv.Atoi(f); err == nil && pt >= 0 && pt <= 127 {
					info.AudioPayloads = append(info.AudioPayloads, uint8(pt))
				}
			}
			info.Codec = ""
			info.HasAudio = true
		case strings.HasPrefix(line, "a=rtcp:"):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "a=rtcp:"))
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				if p, err := strconv.Atoi(fields[0]); err == nil {
					switch media {
					case "audio":
						info.AudioRTCPPort = p
					case "video":
						info.VideoRTCPPort = p
					}
				}
			}
		case strings.HasPrefix(line, "a=rtcp-mux"):
			switch media {
			case "audio":
				info.AudioRTCPMux = true
			case "video":
				info.VideoRTCPMux = true
			}
		case strings.HasPrefix(line, "a=crypto:"):
			c := parseCryptoAttr(strings.TrimPrefix(line, "a=crypto:"))
			switch media {
			case "audio":
				if !info.AudioCrypto.OK {
					info.AudioCrypto = c
				}
			case "video":
				if !info.VideoCrypto.OK {
					info.VideoCrypto = c
				}
			}
		case strings.HasPrefix(line, "a=rtpmap:"):
			rest := strings.TrimPrefix(line, "a=rtpmap:")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				pt, _ := strconv.Atoi(parts[0])
				codec := strings.ToUpper(strings.TrimSpace(strings.SplitN(parts[1], "/", 2)[0]))
				if media == "audio" {
					info.AudioCodecs[uint8(pt)] = codec
				}
				if int(info.PayloadType) == pt {
					info.Codec = codec
				}
				if info.HasVideo && int(info.VideoPT) == pt {
					info.VideoCodec = codec
				}
			}
		}
	}
	return info
}

// rewriteSDP points audio/video media and connection addresses at the relay.
// videoPort <= 0 leaves m=video disabled (port 0) while keeping the m-line.
func rewriteSDP(body []byte, audioPort, videoPort int, ip string) ([]byte, error) {
	return rewriteSDPWithCrypto(body, audioPort, videoPort, ip, "", "")
}

// rewriteSDPWithCrypto rewrites relay ports and replaces per-media SDES crypto
// attributes. crypto strings are the value after "a=crypto:".
func rewriteSDPWithCrypto(body []byte, audioPort, videoPort int, ip, audioCrypto, videoCrypto string) ([]byte, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty sdp")
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	var out []string
	var block []string
	media := ""
	audioSeen := false
	videoPortSet := false

	flush := func() {
		if media == "" {
			return
		}
		crypto := ""
		switch media {
		case "audio":
			crypto = audioCrypto
		case "video":
			if videoPortSet {
				crypto = videoCrypto
			}
		}
		if crypto != "" {
			// SDP media section order is m=, i=, c=, b=, k=, a=.
			// a=crypto is an attribute, so it must come after the fixed
			// media lines, not immediately after m=.
			insert := len(block)
			for i, line := range block {
				if strings.HasPrefix(strings.TrimSpace(line), "a=") {
					insert = i
					break
				}
			}
			cryptoLine := "a=crypto:" + crypto
			block = append(block, "")
			copy(block[insert+1:], block[insert:])
			block[insert] = cryptoLine
		}
		out = append(out, block...)
		block = nil
		media = ""
		videoPortSet = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "m=audio "):
			flush()
			media = "audio"
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				fields[1] = strconv.Itoa(audioPort)
				block = append(block, strings.Join(fields, " "))
				audioSeen = true
			} else {
				block = append(block, line)
			}
		case strings.HasPrefix(trimmed, "m=video "):
			flush()
			media = "video"
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if videoPort > 0 {
					fields[1] = strconv.Itoa(videoPort)
					videoPortSet = true
				} else {
					fields[1] = "0"
				}
				block = append(block, strings.Join(fields, " "))
			} else {
				block = append(block, line)
			}
		case media == "" && strings.HasPrefix(trimmed, "o="):
			out = append(out, rewriteOriginIP(line, ip))
		case media == "" && strings.HasPrefix(trimmed, "c=IN IP4 "):
			out = append(out, "c=IN IP4 "+ip)
		case media != "" && strings.HasPrefix(trimmed, "c=IN IP4 "):
			block = append(block, "c=IN IP4 "+ip)
		case media != "" && strings.HasPrefix(trimmed, "a=rtcp:"):
			rtcpPort := audioPort
			if media == "video" && videoPort > 0 {
				rtcpPort = videoPort
			}
			block = append(block, rewriteRTCPIP(line, ip, rtcpPort))
		case media != "" && strings.HasPrefix(trimmed, "a=crypto:"):
			// Replaced at flush time with the MCS-generated key.
		default:
			if media == "" {
				out = append(out, line)
			} else {
				block = append(block, line)
			}
		}
	}
	flush()
	if !audioSeen {
		return nil, fmt.Errorf("sdp has no audio media")
	}
	return []byte(strings.Join(out, "\r\n") + "\r\n"), nil
}

func rewriteOriginIP(line, ip string) string {
	fields := strings.Fields(line)
	// o=<username> <sess-id> <sess-version> <nettype> <addrtype> <address>
	if len(fields) >= 6 && strings.EqualFold(fields[3], "IN") && strings.EqualFold(fields[4], "IP4") {
		fields[5] = ip
		return strings.Join(fields, " ")
	}
	return line
}

func rewriteRTCPIP(line, ip string, port int) string {
	fields := strings.Fields(line)
	// a=rtcp:<port> [IN IP4 <address>]
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "a=rtcp:") {
		return line
	}
	fields[0] = fmt.Sprintf("a=rtcp:%d", port)
	if len(fields) >= 4 && strings.EqualFold(fields[1], "IN") && strings.EqualFold(fields[2], "IP4") {
		fields[3] = ip
	}
	return strings.Join(fields, " ")
}
