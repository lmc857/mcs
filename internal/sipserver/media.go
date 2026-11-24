package sipserver

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/srtp/v3"
)

// relay forwards RTP between the two call legs and taps payloads for recording.
type relay struct {
	conn      *net.UDPConn
	peer      atomic.Pointer[net.UDPAddr]
	rtcpPeer  atomic.Pointer[net.UDPAddr]
	outPT     atomic.Int32
	once      sync.Once
	rx        *srtp.Context
	tx        *srtp.Context
	codec     string
	transcode bool
	txMu      sync.Mutex
	rxRTP     atomic.Uint64
	txRTP     atomic.Uint64
	rxRTCP    atomic.Uint64
	txRTCP    atomic.Uint64
}

func newRelay() (*relay, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	r := &relay{conn: conn}
	r.outPT.Store(-1)
	return r, nil
}

func (r *relay) port() int { return r.conn.LocalAddr().(*net.UDPAddr).Port }

func (r *relay) setPeer(host string, port int) {
	r.setPeerRTCP(host, port, 0, false)
}

// setPeerRTCP sets the RTP destination and, separately, the RTCP destination.
// Sending RTCP to the RTP port (or letting an incoming RTCP packet overwrite
// the RTP peer) causes intermittent loss and visible video corruption.
func (r *relay) setPeerRTCP(host string, rtpPort, rtcpPort int, mux bool) {
	if host == "" || rtpPort <= 0 {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return
	}
	r.peer.Store(&net.UDPAddr{IP: ip, Port: rtpPort})
	if mux || rtcpPort <= 0 {
		rtcpPort = rtpPort
	}
	r.rtcpPeer.Store(&net.UDPAddr{IP: ip, Port: rtcpPort})
}

// setOutPT rewrites the RTP payload type on outgoing media. It is used when
// the two legs negotiated different dynamic H264 payload types.
func (r *relay) setOutPT(pt uint8) {
	if pt <= 127 {
		r.outPT.Store(int32(pt))
	}
}

func (r *relay) writeToPeer(pkt []byte) error {
	return r.send(pkt)
}

func (r *relay) send(pkt []byte) error {
	p := r.peer.Load()
	if isRTCP(pkt) {
		if rp := r.rtcpPeer.Load(); rp != nil {
			p = rp
		}
	}
	if p == nil {
		return fmt.Errorf("relay peer not learned")
	}
	if r.tx != nil {
		r.txMu.Lock()
		var enc []byte
		var err error
		if isRTCP(pkt) {
			enc, err = r.tx.EncryptRTCP(nil, pkt, nil)
		} else {
			enc, err = r.tx.EncryptRTP(nil, pkt, nil)
		}
		r.txMu.Unlock()
		if err != nil {
			return err
		}
		pkt = enc
	}
	_, err := r.conn.WriteToUDP(pkt, p)
	if err == nil {
		if isRTCP(pkt) {
			r.txRTCP.Add(1)
		} else {
			r.txRTP.Add(1)
		}
	}
	return err
}

// transcodeRTP converts G.711 A-law/u-law between legs and normalizes the
// payload type. Other codecs pass through untouched.
func transcodeRTP(pkt []byte, from, to string) []byte {
	if len(pkt) < 12 {
		return pkt
	}
	from = strings.ToUpper(from)
	to = strings.ToUpper(to)
	if from == to && from != "PCMU" && from != "PCMA" {
		return pkt
	}
	payload := pkt[12:]
	out := make([]byte, 12+len(payload))
	copy(out[:12], pkt[:12])
	switch {
	case to == "PCMU" && from == "PCMA":
		samples := make([]int16, len(payload))
		for i, b := range payload {
			samples[i] = decodeALaw(b)
		}
		copy(out[12:], encodeULaw(samples))
	case to == "PCMA" && from == "PCMU":
		samples := make([]int16, len(payload))
		for i, b := range payload {
			samples[i] = decodeULaw(b)
		}
		copy(out[12:], encodeALaw(samples))
	default:
		copy(out[12:], payload)
	}
	if to == "PCMU" {
		out[1] = (out[1] & 0x80) | 0
	} else if to == "PCMA" {
		out[1] = (out[1] & 0x80) | 8
	}
	return out
}

func isRTCP(pkt []byte) bool {
	return len(pkt) >= 2 && pkt[1] >= 192 && pkt[1] <= 223
}

func (r *relay) close() {
	r.once.Do(func() { _ = r.conn.Close() })
}

func (r *relay) start(dst *relay, onPayload func([]byte, uint8)) {
	r.startWithTap(dst, onPayload, nil)
}

func (r *relay) startWithTap(dst *relay, onPayload func([]byte, uint8), onPacket func([]byte)) {
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := r.conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 8 {
				continue
			}
			pkt := buf[:n]
			rtcp := isRTCP(pkt)
			if !rtcp && n < 12 {
				continue
			}
			if rtcp {
				// RTCP often comes from RTP+1. Never use it to overwrite
				// the RTP destination; only remember where RTCP replies go.
				r.rtcpPeer.Store(addr)
			} else {
				r.peer.Store(addr)
			}
			if r.rx != nil {
				var dec []byte
				var err error
				if rtcp {
					dec, err = r.rx.DecryptRTCP(nil, pkt, nil)
				} else {
					dec, err = r.rx.DecryptRTP(nil, pkt, nil)
				}
				if err != nil {
					continue
				}
				pkt = dec
			}
			if rtcp {
				r.rxRTCP.Add(1)
				_ = dst.send(pkt)
				continue
			}
			r.rxRTP.Add(1)
			if onPacket != nil {
				clone := make([]byte, len(pkt))
				copy(clone, pkt)
				onPacket(clone)
			}
			if onPayload != nil {
				if payload, pt, ok := rtpPayload(pkt); ok {
					onPayload(payload, pt)
				}
			}
			out := pkt
			if rtcp {
				out = pkt
			} else {
				if pt := uint8(r.outPT.Load()); r.outPT.Load() >= 0 && len(out) >= 2 {
					out[1] = (out[1] & 0x80) | (pt & 0x7f)
				}
				if dst.transcode {
					out = transcodeRTP(pkt, r.codec, dst.codec)
				}
			}
			if err := dst.send(out); err != nil {
				continue
			}
		}
	}()
}

func rtpPayload(pkt []byte) ([]byte, uint8, bool) {
	if len(pkt) < 12 {
		return nil, 0, false
	}
	pt := pkt[1] & 0x7f
	if pt >= 192 { // RTCP
		return nil, 0, false
	}
	off := 12 + 4*int(pkt[0]&0x0f)
	if off > len(pkt) {
		return nil, 0, false
	}
	if pkt[0]&0x10 != 0 { // header extension
		if len(pkt) < off+4 {
			return nil, 0, false
		}
		extLen := int(binary.BigEndian.Uint16(pkt[off+2 : off+4]))
		off += 4 + 4*extLen
		if off > len(pkt) {
			return nil, 0, false
		}
	}
	end := len(pkt)
	if pkt[0]&0x20 != 0 { // padding
		pad := int(pkt[len(pkt)-1])
		if pad > 0 && pad <= end-off {
			end -= pad
		}
	}
	return pkt[off:end], pt, true
}

// recorder writes two mono raw streams, then interleaves them into one WAV.
type recorder struct {
	mu           sync.Mutex
	dir          string
	id           string
	a, b         *os.File
	aPath, bPath string
	closed       bool
}

func newRecorder(dataDir, id string) (*recorder, error) {
	tmp := filepath.Join(dataDir, "recordings", ".tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	aPath := filepath.Join(tmp, id+".a.raw")
	bPath := filepath.Join(tmp, id+".b.raw")
	a, err := os.Create(aPath)
	if err != nil {
		return nil, err
	}
	b, err := os.Create(bPath)
	if err != nil {
		_ = a.Close()
		return nil, err
	}
	return &recorder{dir: dataDir, id: id, a: a, b: b, aPath: aPath, bPath: bPath}, nil
}

func (r *recorder) writeA(payload []byte, codec string) { r.write(r.a, payload, codec) }
func (r *recorder) writeB(payload []byte, codec string) { r.write(r.b, payload, codec) }

func (r *recorder) write(f *os.File, payload []byte, codec string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || f == nil {
		return
	}
	_, _ = f.Write(decodeG711(payload, codec))
}

// close returns a relative URL path (under /recordings/) and removes raw files.
func (r *recorder) close() string {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ""
	}
	r.closed = true
	_ = r.a.Close()
	_ = r.b.Close()
	aPath, bPath, id := r.aPath, r.bPath, r.id
	r.mu.Unlock()

	fa, errA := os.Open(aPath)
	fb, errB := os.Open(bPath)
	if errA != nil || errB != nil {
		if fa != nil {
			_ = fa.Close()
		}
		if fb != nil {
			_ = fb.Close()
		}
		_ = os.Remove(aPath)
		_ = os.Remove(bPath)
		return ""
	}
	defer fa.Close()
	defer fb.Close()
	defer os.Remove(aPath)
	defer os.Remove(bPath)

	statA, _ := fa.Stat()
	statB, _ := fb.Stat()
	if (statA == nil || statA.Size() == 0) && (statB == nil || statB.Size() == 0) {
		return ""
	}

	weekday := time.Now()
	relDir := filepath.Join("recordings", weekday.Format("2006"), weekday.Format("01"), weekday.Format("02"))
	finalDir := filepath.Join(r.dir, relDir)
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return ""
	}
	finalPath := filepath.Join(finalDir, id+".wav")
	out, err := os.Create(finalPath)
	if err != nil {
		return ""
	}
	if _, err := out.Write(wavHeader(0)); err != nil {
		_ = out.Close()
		return ""
	}

	ra := bufio.NewReader(fa)
	rb := bufio.NewReader(fb)
	bufA := make([]byte, 320)
	bufB := make([]byte, 320)
	var dataSize uint32
	for {
		nA, _ := io.ReadFull(ra, bufA)
		nB, _ := io.ReadFull(rb, bufB)
		if nA == 0 && nB == 0 {
			break
		}
		n := nA
		if nB > n {
			n = nB
		}
		outBuf := make([]byte, n*2)
		for i := 0; i+1 < n; i += 2 {
			var l, rr int16
			if i+1 < nA {
				l = int16(binary.LittleEndian.Uint16(bufA[i : i+2]))
			}
			if i+1 < nB {
				rr = int16(binary.LittleEndian.Uint16(bufB[i : i+2]))
			}
			binary.LittleEndian.PutUint16(outBuf[i*2:], uint16(l))
			binary.LittleEndian.PutUint16(outBuf[i*2+2:], uint16(rr))
		}
		if _, err := out.Write(outBuf); err != nil {
			break
		}
		dataSize += uint32(len(outBuf))
	}
	if _, err := out.Seek(0, 0); err == nil {
		_, _ = out.Write(wavHeader(dataSize))
	}
	_ = out.Close()
	if dataSize == 0 {
		_ = os.Remove(finalPath)
		return ""
	}
	return "/" + filepath.ToSlash(filepath.Join(relDir, id+".wav"))
}

func wavHeader(dataSize uint32) []byte {
	const (
		sampleRate = 8000
		channels   = 2
		bits       = 16
	)
	byteRate := uint32(sampleRate * channels * bits / 8)
	blockAlign := uint16(channels * bits / 8)
	h := make([]byte, 44)
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], 36+dataSize)
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16)
	binary.LittleEndian.PutUint16(h[20:22], 1)
	binary.LittleEndian.PutUint16(h[22:24], channels)
	binary.LittleEndian.PutUint32(h[24:28], sampleRate)
	binary.LittleEndian.PutUint32(h[28:32], byteRate)
	binary.LittleEndian.PutUint16(h[32:34], blockAlign)
	binary.LittleEndian.PutUint16(h[34:36], bits)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], dataSize)
	return h
}

func decodeG711(payload []byte, codec string) []byte {
	out := make([]byte, len(payload)*2)
	alaw := strings.Contains(strings.ToUpper(codec), "PCMA")
	for i, b := range payload {
		var s int16
		if alaw {
			s = decodeALaw(b)
		} else {
			s = decodeULaw(b)
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

func decodeULaw(b byte) int16 {
	u := ^b
	sign := u & 0x80
	exp := (u >> 4) & 0x07
	mant := u & 0x0F
	sample := int16(((int(mant) << 3) + 0x84) << exp)
	sample -= 0x84
	if sign != 0 {
		sample = -sample
	}
	return sample
}

func decodeALaw(b byte) int16 {
	a := b ^ 0x55
	sign := a & 0x80
	exp := (a >> 4) & 0x07
	mant := a & 0x0F
	var sample int16
	if exp == 0 {
		sample = int16((int(mant) << 4) + 8)
	} else {
		sample = int16(((int(mant) << 4) + 0x108) << (exp - 1))
	}
	if sign == 0 {
		sample = -sample
	}
	return sample
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("advertise address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("advertise port %q: %w", portStr, err)
	}
	return host, port, nil
}
