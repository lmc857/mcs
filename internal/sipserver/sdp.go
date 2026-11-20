package sipserver

import (
	"fmt"
	"strconv"
	"strings"
)

type sdpInfo struct {
	AudioPort   int
	VideoPort   int
	ConnIP      string
	PayloadType uint8
	Codec       string
	VideoPT     uint8
	VideoCodec  string
	AudioProto  string
	VideoProto  string
	AudioSecure bool
	VideoSecure bool
	AudioCrypto cryptoInfo
	VideoCrypto cryptoInfo
	HasAudio    bool
	HasVideo    bool
}

func parseSDP(body []byte) sdpInfo {
	info := sdpInfo{}
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
			info.Codec = ""
			info.HasAudio = true
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
	audioSeen := false
	videoSeen := false
	media := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "c=IN IP4 "):
			out = append(out, "c=IN IP4 "+ip)
		case strings.HasPrefix(trimmed, "m=audio "):
			media = "audio"
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				fields[1] = strconv.Itoa(audioPort)
				out = append(out, strings.Join(fields, " "))
				if audioCrypto != "" {
					out = append(out, "a=crypto:"+audioCrypto)
				}
				audioSeen = true
			} else {
				out = append(out, line)
			}
		case strings.HasPrefix(trimmed, "m=video "):
			media = "video"
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if videoPort > 0 {
					fields[1] = strconv.Itoa(videoPort)
				} else {
					fields[1] = "0"
				}
				out = append(out, strings.Join(fields, " "))
				if videoCrypto != "" {
					out = append(out, "a=crypto:"+videoCrypto)
				}
				videoSeen = true
			} else {
				out = append(out, line)
			}
		case strings.HasPrefix(trimmed, "a=crypto:"):
			_ = media
			// replaced above with MCS's own crypto attribute
		default:
			out = append(out, line)
		}
	}
	if !audioSeen {
		return nil, fmt.Errorf("sdp has no audio media")
	}
	_ = videoSeen
	return []byte(strings.Join(out, "\r\n") + "\r\n"), nil
}
